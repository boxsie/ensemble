package transport

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"google.golang.org/protobuf/proto"

	"github.com/boxsie/ensemble/internal/identity"
	protoc "github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

const (
	presenceProto    = protocol.ID(protoc.ProtocolPresence)
	pingTimeout      = 5 * time.Second
	defaultPingEvery = 30 * time.Second
)

// OnlineStatus reports a peer's presence state.
type OnlineStatus struct {
	Address string
	Online  bool
}

// PeerLister returns all currently connected peers.
type PeerLister interface {
	ActivePeers() []*PeerConnection
}

// Presence handles ping/pong health checks for connected peers.
type Presence struct {
	host    *Host
	keypair *identity.Keypair

	mu     sync.RWMutex
	online map[string]bool // address → online
}

// NewPresence creates a presence service and registers the pong handler.
func NewPresence(host *Host, kp *identity.Keypair) *Presence {
	p := &Presence{
		host:    host,
		keypair: kp,
		online:  make(map[string]bool),
	}
	host.SetStreamHandler(presenceProto, p.handlePing)
	return p
}

// Close unregisters the stream handler.
func (p *Presence) Close() {
	p.host.RemoveStreamHandler(presenceProto)
}

// IsOnline returns whether a peer is currently marked as online.
func (p *Presence) IsOnline(addr string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.online[addr]
}

// PingPeer sends a ping and waits for a pong. Returns round-trip time.
func (p *Presence) PingPeer(ctx context.Context, pid peer.ID) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	stream, err := p.host.OpenStream(ctx, pid, presenceProto)
	if err != nil {
		return 0, fmt.Errorf("opening presence stream: %w", err)
	}
	defer stream.Close()

	now := time.Now()
	ping := &pb.Ping{Timestamp: now.UnixMilli()}
	payload, err := proto.Marshal(ping)
	if err != nil {
		return 0, fmt.Errorf("marshaling ping: %w", err)
	}

	env := protoc.WrapMessage(pb.MessageType_PING, payload, p.keypair)

	stream.SetWriteDeadline(time.Now().Add(pingTimeout))
	if err := protoc.WriteMsg(stream, env); err != nil {
		stream.Reset()
		return 0, fmt.Errorf("sending ping: %w", err)
	}

	stream.SetReadDeadline(time.Now().Add(pingTimeout))
	respEnv, err := protoc.ReadMsg(stream)
	if err != nil {
		stream.Reset()
		return 0, fmt.Errorf("reading pong: %w", err)
	}

	if respEnv.Type != pb.MessageType_PONG {
		return 0, fmt.Errorf("unexpected response type: %v", respEnv.Type)
	}

	return time.Since(now), nil
}

// Start begins periodic pinging of all connected peers.
// onChange is called when a peer's online status changes.
// Blocks until ctx is cancelled.
func (p *Presence) Start(ctx context.Context, lister PeerLister, onChange func(OnlineStatus)) {
	ticker := time.NewTicker(defaultPingEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pingAll(ctx, lister, onChange)
		}
	}
}

func (p *Presence) pingAll(ctx context.Context, lister PeerLister, onChange func(OnlineStatus)) {
	peers := lister.ActivePeers()
	for _, pc := range peers {
		_, err := p.PingPeer(ctx, pc.PeerID)
		nowOnline := err == nil

		p.mu.Lock()
		wasOnline := p.online[pc.Address]
		p.online[pc.Address] = nowOnline
		p.mu.Unlock()

		if nowOnline != wasOnline {
			onChange(OnlineStatus{Address: pc.Address, Online: nowOnline})
		}
	}
}

// handlePing responds to incoming pings with a pong.
func (p *Presence) handlePing(stream network.Stream) {
	defer stream.Close()

	stream.SetReadDeadline(time.Now().Add(pingTimeout))
	env, err := protoc.ReadMsg(stream)
	if err != nil {
		return
	}

	if env.Type != pb.MessageType_PING {
		return
	}

	ping := &pb.Ping{}
	if err := proto.Unmarshal(env.Payload, ping); err != nil {
		return
	}

	pong := &pb.Pong{EchoTimestamp: ping.Timestamp}
	pongPayload, err := proto.Marshal(pong)
	if err != nil {
		return
	}

	pongEnv := protoc.WrapMessage(pb.MessageType_PONG, pongPayload, p.keypair)

	stream.SetWriteDeadline(time.Now().Add(pingTimeout))
	protoc.WriteMsg(stream, pongEnv)
}
