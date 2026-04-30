package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
)

const (
	// RecordTTL is the maximum age of a DHT record before it's considered expired.
	RecordTTL = 24 * time.Hour

	// LookupMaxIterations is the maximum rounds of iterative lookup.
	LookupMaxIterations = 20
)

// Dialer abstracts network dialing for DHT connections.
type Dialer interface {
	DialContext(ctx context.Context, address string) (net.Conn, error)
}

// DHTDiscovery handles peer announcement and lookup over the DHT.
// DHT records are hash-only (address + onion, no public keys) for quantum resistance.
type DHTDiscovery struct {
	rt        *RoutingTable
	localPeer *PeerInfo
	dialer    Dialer
	recordTTL time.Duration
}

// NewDHTDiscovery creates a new DHT discovery instance.
func NewDHTDiscovery(rt *RoutingTable, localPeer *PeerInfo, dialer Dialer) *DHTDiscovery {
	return &DHTDiscovery{
		rt:        rt,
		localPeer: localPeer,
		dialer:    dialer,
		recordTTL: RecordTTL,
	}
}

// RT returns the underlying routing table.
func (d *DHTDiscovery) RT() *RoutingTable {
	return d.rt
}

// Bootstrap dials a seed node and performs a FindNode(self) to populate the routing table.
// The seed includes itself in the response, so the caller always learns the seed's identity
// and can later announce to it. Returns the number of new peers added to the routing table.
func (d *DHTDiscovery) Bootstrap(ctx context.Context, onionAddr string) (int, error) {
	log.Printf("dht: bootstrap starting (seed=%s)", onionAddr)
	seed := &PeerInfo{OnionAddr: onionAddr}
	peers, err := d.sendFindNode(ctx, seed, d.localPeer.ID)
	if err != nil {
		log.Printf("dht: bootstrap failed (seed=%s): %v", onionAddr, err)
		return 0, fmt.Errorf("bootstrap find_node: %w", err)
	}
	for _, p := range peers {
		d.rt.AddPeer(p, p.ID)
	}
	log.Printf("dht: bootstrap complete (seed=%s, peers_found=%d, rt_size=%d)", onionAddr, len(peers), d.rt.Size())
	return len(peers), nil
}

// Announce pushes our PeerRecord to the K closest nodes in the routing table.
func (d *DHTDiscovery) Announce(ctx context.Context) error {
	d.localPeer.LastSeen = time.Now()
	closest := d.rt.FindClosest(d.localPeer.ID, K)
	if len(closest) == 0 {
		return fmt.Errorf("no peers in routing table to announce to")
	}

	log.Printf("dht: announcing to %d peers", len(closest))
	var succeeded int
	for _, peer := range closest {
		if err := d.sendPutRecord(ctx, peer); err == nil {
			succeeded++
		} else {
			log.Printf("dht: announce to %s failed: %v", peer.OnionAddr, err)
		}
	}
	if succeeded == 0 {
		return fmt.Errorf("announce failed: all %d peers unreachable", len(closest))
	}
	log.Printf("dht: announce complete (%d/%d succeeded)", succeeded, len(closest))
	return nil
}

// Lookup performs an iterative DHT lookup for the given address.
// Returns the PeerInfo if found, or an error if not found.
func (d *DHTDiscovery) Lookup(ctx context.Context, addr string) (*PeerInfo, error) {
	log.Printf("dht: lookup starting (addr=%s)", addr)
	target, err := NodeIDFromAddress(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	// Check local routing table first.
	closest := d.rt.FindClosest(target, K)
	for _, p := range closest {
		if p.Address == addr && !d.isExpired(p) {
			log.Printf("dht: lookup hit in local routing table (addr=%s, onion=%s)", addr, p.OnionAddr)
			return p, nil
		}
	}

	// Iterative lookup: query progressively closer nodes.
	queried := make(map[NodeID]bool)
	for range LookupMaxIterations {
		candidates := d.rt.FindClosest(target, K)
		progressed := false

		for _, peer := range candidates {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			if queried[peer.ID] {
				continue
			}
			queried[peer.ID] = true
			progressed = true

			peers, err := d.sendFindNode(ctx, peer, target)
			if err != nil {
				continue
			}

			for _, found := range peers {
				if found.Address == addr && !d.isExpired(found) {
					log.Printf("dht: lookup found peer (addr=%s, onion=%s, queried=%d)", addr, found.OnionAddr, len(queried))
					return found, nil
				}
				d.rt.AddPeer(found, peer.ID)
			}
		}

		if !progressed {
			break
		}
	}

	log.Printf("dht: lookup failed (addr=%s, queried=%d)", addr, len(queried))
	return nil, fmt.Errorf("peer not found: %s", addr)
}

// HandleConn processes an incoming DHT connection (one request-response per conn).
func (d *DHTDiscovery) HandleConn(conn net.Conn) {
	defer conn.Close()

	env, err := protocol.ReadMsg(conn)
	if err != nil {
		return
	}

	d.HandleEnvelope(conn, env)
}

// HandleEnvelope processes a pre-read DHT envelope on an existing connection.
func (d *DHTDiscovery) HandleEnvelope(conn net.Conn, env *pb.Envelope) {
	switch env.Type {
	case pb.MessageType_DHT_FIND_NODE:
		d.handleFindNode(conn, env)
	case pb.MessageType_DHT_PUT_RECORD:
		d.handlePutRecord(conn, env)
	}
}

func (d *DHTDiscovery) handleFindNode(conn net.Conn, env *pb.Envelope) {
	var msg pb.DHTFindNode
	if err := proto.Unmarshal(env.Payload, &msg); err != nil {
		return
	}

	var target NodeID
	if len(msg.Target) >= IDLength {
		copy(target[:], msg.Target)
	}

	closest := d.rt.FindClosest(target, K)

	// Include ourselves in the response so the caller learns our identity.
	// This is essential for bootstrap: if our RT is empty, the caller still
	// gets at least one peer (us) to announce to.
	self := *d.localPeer
	self.LastSeen = time.Now()
	closest = append(closest, &self)

	log.Printf("dht: handleFindNode from=%s target=%s returning=%d", conn.RemoteAddr(), target, len(closest))
	resp := peersToResponse(closest)
	payload, err := proto.Marshal(resp)
	if err != nil {
		return
	}
	protocol.WriteMsg(conn, wrapUnsigned(pb.MessageType_DHT_NODE_RESPONSE, payload))
}

func (d *DHTDiscovery) handlePutRecord(conn net.Conn, env *pb.Envelope) {
	var msg pb.DHTPutRecord
	if err := proto.Unmarshal(env.Payload, &msg); err != nil {
		return
	}

	peer := recordToPeerInfo(msg.Record)
	if peer == nil {
		return
	}

	if d.isExpired(peer) {
		log.Printf("dht: handlePutRecord rejected expired record (addr=%s)", peer.Address)
		return
	}

	// Store the record (source = the peer itself for self-announcements).
	d.rt.AddPeer(peer, peer.ID)
	log.Printf("dht: handlePutRecord stored (addr=%s, onion=%s, rt_size=%d)", peer.Address, peer.OnionAddr, d.rt.Size())

	// Respond with closest peers to the announced node.
	closest := d.rt.FindClosest(peer.ID, K)
	resp := peersToResponse(closest)
	payload, err := proto.Marshal(resp)
	if err != nil {
		return
	}
	protocol.WriteMsg(conn, wrapUnsigned(pb.MessageType_DHT_NODE_RESPONSE, payload))
}

func (d *DHTDiscovery) sendFindNode(ctx context.Context, peer *PeerInfo, target NodeID) ([]*PeerInfo, error) {
	conn, err := d.dialer.DialContext(ctx, peer.OnionAddr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", peer.OnionAddr, err)
	}
	defer conn.Close()

	msg := &pb.DHTFindNode{Target: target[:]}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshalling find node: %w", err)
	}
	if err := protocol.WriteMsg(conn, wrapUnsigned(pb.MessageType_DHT_FIND_NODE, payload)); err != nil {
		return nil, fmt.Errorf("writing find node: %w", err)
	}

	env, err := protocol.ReadMsg(conn)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var resp pb.DHTNodeResponse
	if err := proto.Unmarshal(env.Payload, &resp); err != nil {
		return nil, fmt.Errorf("unmarshalling response: %w", err)
	}

	var peers []*PeerInfo
	for _, r := range resp.Peers {
		p := recordToPeerInfo(r)
		if p != nil && !d.isExpired(p) {
			peers = append(peers, p)
		}
	}
	return peers, nil
}

func (d *DHTDiscovery) sendPutRecord(ctx context.Context, target *PeerInfo) error {
	conn, err := d.dialer.DialContext(ctx, target.OnionAddr)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", target.OnionAddr, err)
	}
	defer conn.Close()

	record := peerInfoToRecord(d.localPeer)
	msg := &pb.DHTPutRecord{Record: record}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling put record: %w", err)
	}
	if err := protocol.WriteMsg(conn, wrapUnsigned(pb.MessageType_DHT_PUT_RECORD, payload)); err != nil {
		return fmt.Errorf("writing put record: %w", err)
	}

	// Read response — may contain closer peers to add to our routing table.
	env, err := protocol.ReadMsg(conn)
	if err != nil {
		return nil // announce succeeded even if we can't read response
	}

	var resp pb.DHTNodeResponse
	if err := proto.Unmarshal(env.Payload, &resp); err != nil {
		return nil
	}

	for _, r := range resp.Peers {
		p := recordToPeerInfo(r)
		if p != nil {
			d.rt.AddPeer(p, target.ID)
		}
	}

	return nil
}

func (d *DHTDiscovery) isExpired(p *PeerInfo) bool {
	return time.Since(p.LastSeen) > d.recordTTL
}

// Conversion helpers.

func peerInfoToRecord(p *PeerInfo) *pb.PeerRecord {
	return &pb.PeerRecord{
		NodeId:       p.ID[:],
		Address:      p.Address,
		OnionAddress: p.OnionAddr,
		Timestamp:    p.LastSeen.UnixMilli(),
	}
}

func recordToPeerInfo(r *pb.PeerRecord) *PeerInfo {
	if r == nil || len(r.NodeId) < IDLength {
		return nil
	}
	var id NodeID
	copy(id[:], r.NodeId)
	return &PeerInfo{
		ID:        id,
		Address:   r.Address,
		OnionAddr: r.OnionAddress,
		LastSeen:  time.UnixMilli(r.Timestamp),
	}
}

func peersToResponse(peers []*PeerInfo) *pb.DHTNodeResponse {
	var records []*pb.PeerRecord
	for _, p := range peers {
		records = append(records, peerInfoToRecord(p))
	}
	return &pb.DHTNodeResponse{Peers: records}
}

func wrapUnsigned(msgType pb.MessageType, payload []byte) *pb.Envelope {
	return &pb.Envelope{
		Type:      msgType,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}
}
