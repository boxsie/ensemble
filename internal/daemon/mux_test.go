package daemon

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/discovery"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/signaling"
	"google.golang.org/protobuf/proto"
)

// testMuxDialer maps fake onion addresses to local TCP addresses.
type testMuxDialer struct {
	addrs map[string]string
}

func (d *testMuxDialer) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	localAddr, ok := d.addrs[addr]
	if !ok {
		return nil, net.ErrClosed
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", localAddr)
}

// setupMux creates a listener with the onion multiplexer serving both DHT and signaling.
// Returns the listener address, DHT instance, signaling server, and cleanup func.
func setupMux(t *testing.T) (lnAddr string, dht *discovery.DHTDiscovery, sig *signaling.Server, kp *identity.Keypair, addr string) {
	t.Helper()

	kp, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	addr = identity.DeriveAddress(kp.PublicKey()).String()

	// DHT setup.
	nodeID := discovery.NodeIDFromPublicKey(kp.PublicKey())
	rt := discovery.NewRoutingTable(nodeID)
	localPeer := &discovery.PeerInfo{
		ID:        nodeID,
		Address:   addr,
		OnionAddr: "test.onion",
		LastSeen:  time.Now(),
	}
	dialer := &testMuxDialer{addrs: make(map[string]string)}
	dht = discovery.NewDHTDiscovery(rt, localPeer, dialer)

	// Add a peer to the DHT so FindNode returns something.
	otherKp, _ := identity.Generate()
	otherAddr := identity.DeriveAddress(otherKp.PublicKey()).String()
	otherID := discovery.NodeIDFromPublicKey(otherKp.PublicKey())
	otherPeer := &discovery.PeerInfo{
		ID:        otherID,
		Address:   otherAddr,
		OnionAddr: "other.onion",
		LastSeen:  time.Now(),
	}
	rt.AddPeer(otherPeer, otherPeer.ID)

	// Signaling setup.
	cs := contacts.NewStore(t.TempDir())
	sig = signaling.NewServer(kp, addr, cs)

	// Start multiplexed listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lnAddr = ln.Addr().String()

	d := &Daemon{}
	ctx, cancel := context.WithCancel(context.Background())
	go d.serveOnion(ctx, ln, dht, sig)
	t.Cleanup(func() {
		cancel()
		ln.Close()
	})

	return lnAddr, dht, sig, kp, addr
}

func TestMux_RoutesDHTFindNode(t *testing.T) {
	lnAddr, _, _, _, _ := setupMux(t)

	// Dial the multiplexed listener and send a DHT FindNode request.
	conn, err := net.DialTimeout("tcp", lnAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Build a FindNode message.
	target := make([]byte, discovery.IDLength)
	msg := &pb.DHTFindNode{Target: target}
	payload, _ := proto.Marshal(msg)
	env := &pb.Envelope{
		Type:      pb.MessageType_DHT_FIND_NODE,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}

	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatalf("writing FindNode: %v", err)
	}

	// Read response — should be a DHT_NODE_RESPONSE.
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.Type != pb.MessageType_DHT_NODE_RESPONSE {
		t.Fatalf("expected DHT_NODE_RESPONSE, got %v", resp.Type)
	}

	var nodeResp pb.DHTNodeResponse
	if err := proto.Unmarshal(resp.Payload, &nodeResp); err != nil {
		t.Fatalf("unmarshalling response: %v", err)
	}

	// Should contain at least the one peer we added.
	if len(nodeResp.Peers) == 0 {
		t.Fatal("expected at least one peer in FindNode response")
	}
}

func TestMux_RoutesDHTPutRecord(t *testing.T) {
	lnAddr, dht, _, _, _ := setupMux(t)

	// Build a PutRecord message for a new peer.
	newKp, _ := identity.Generate()
	newAddr := identity.DeriveAddress(newKp.PublicKey()).String()
	newID := discovery.NodeIDFromPublicKey(newKp.PublicKey())

	record := &pb.PeerRecord{
		NodeId:       newID[:],
		Address:      newAddr,
		OnionAddress: "new-peer.onion",
		Timestamp:    time.Now().UnixMilli(),
	}
	putMsg := &pb.DHTPutRecord{Record: record}
	payload, _ := proto.Marshal(putMsg)
	env := &pb.Envelope{
		Type:      pb.MessageType_DHT_PUT_RECORD,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}

	conn, err := net.DialTimeout("tcp", lnAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatalf("writing PutRecord: %v", err)
	}

	// Read response.
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.Type != pb.MessageType_DHT_NODE_RESPONSE {
		t.Fatalf("expected DHT_NODE_RESPONSE, got %v", resp.Type)
	}

	// Verify the record was stored in the routing table.
	time.Sleep(50 * time.Millisecond)
	closest := dht.RT().FindClosest(newID, 1)
	found := false
	for _, p := range closest {
		if p.Address == newAddr {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("PutRecord should have stored the peer in the routing table")
	}
}

func TestMux_RoutesSignalingConnRequest(t *testing.T) {
	lnAddr, _, sig, _, serverAddr := setupMux(t)

	// Set up the signaling server to auto-reject (no contacts, no handler).
	// We just need to verify it processes the message (doesn't EOF like before).

	// Generate initiator identity.
	initKp, _ := identity.Generate()
	initAddr := identity.DeriveAddress(initKp.PublicKey()).String()

	// Add initiator as a contact so signaling auto-accepts.
	sig.OnConnectionRequest(func(cr *signaling.ConnectionRequest) {
		cr.Decision <- signaling.ConnectionDecision{Accepted: true}
	})

	conn, err := net.DialTimeout("tcp", lnAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Build a ConnRequest.
	reqData, _, err := signaling.CreateConnRequest(initAddr, serverAddr)
	if err != nil {
		t.Fatalf("CreateConnRequest: %v", err)
	}
	env := &pb.Envelope{
		Type:    pb.MessageType_CONN_REQUEST,
		Payload: reqData,
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatalf("writing ConnRequest: %v", err)
	}

	// Read response — should be CONN_RESPONSE (not EOF).
	resp, err := protocol.ReadMsg(conn)
	if err != nil {
		t.Fatalf("reading ConnResponse: %v (would be EOF if mux didn't route to signaling)", err)
	}
	if resp.Type != pb.MessageType_CONN_RESPONSE {
		t.Fatalf("expected CONN_RESPONSE, got %v", resp.Type)
	}
}

func TestMux_UnknownMessageTypeDropped(t *testing.T) {
	lnAddr, _, _, _, _ := setupMux(t)

	conn, err := net.DialTimeout("tcp", lnAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	// Send an envelope with an unrouted message type.
	env := &pb.Envelope{
		Type:      pb.MessageType_CHAT_TEXT, // not DHT or signaling
		Payload:   []byte("hello"),
		Timestamp: time.Now().UnixMilli(),
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// The mux should close the connection without sending a response.
	_, err = protocol.ReadMsg(conn)
	if err == nil {
		t.Fatal("expected EOF for unrouted message type")
	}
}
