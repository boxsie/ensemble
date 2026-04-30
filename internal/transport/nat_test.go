package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/signaling"
)

func TestNATTraverseDirectSuccess(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	hostA, err := NewHost(HostConfig{Keypair: kpA, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostA.Close()

	hostB, err := NewHost(HostConfig{Keypair: kpB, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := NATTraverse(ctx, hostA, hostB.AddrInfo(), DefaultNATConfig())

	if result.Strategy != StrategyDirect {
		t.Fatalf("expected direct strategy, got %v", result.Strategy)
	}
	if len(result.Addrs) == 0 {
		t.Fatal("expected at least one address")
	}
	if result.Latency == 0 {
		t.Error("expected non-zero latency")
	}
}

func TestNATTraverseFallsToTorOnly(t *testing.T) {
	kpA, _ := identity.Generate()

	hostA, err := NewHost(HostConfig{Keypair: kpA, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostA.Close()

	// Create an unreachable peer (random ID, no valid addrs).
	kpB, _ := identity.Generate()
	libKey, _ := kpB.ToLibp2pKey()
	peerID, _ := peer.IDFromPrivateKey(libKey)
	bogusAddr, _ := ma.NewMultiaddr("/ip4/192.0.2.1/udp/9999/quic-v1")

	unreachable := peer.AddrInfo{
		ID:    peerID,
		Addrs: []ma.Multiaddr{bogusAddr},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := NATConfig{
		DirectTimeout:    500 * time.Millisecond,
		HolePunchTimeout: 500 * time.Millisecond,
		RelayTimeout:     500 * time.Millisecond,
	}

	result := NATTraverse(ctx, hostA, unreachable, cfg)

	if result.Strategy != StrategyTorOnly {
		t.Fatalf("expected tor-only fallback, got %v", result.Strategy)
	}
}

func TestNATStrategyString(t *testing.T) {
	tests := []struct {
		s    NATStrategy
		want string
	}{
		{StrategyDirect, "direct"},
		{StrategyHolePunch, "holepunch"},
		{StrategyRelay, "relay"},
		{StrategyTorOnly, "tor-only"},
		{NATStrategy(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Strategy(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestFilterNonRelay(t *testing.T) {
	direct, _ := ma.NewMultiaddr("/ip4/1.2.3.4/udp/4001/quic-v1")
	relay, _ := ma.NewMultiaddr("/ip4/5.6.7.8/udp/4001/quic-v1/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN/p2p-circuit")

	pi := peer.AddrInfo{
		ID:    "test-peer",
		Addrs: []ma.Multiaddr{direct, relay},
	}

	filtered := filterNonRelay(pi)
	if len(filtered.Addrs) != 1 {
		t.Fatalf("expected 1 direct addr, got %d", len(filtered.Addrs))
	}
	if filtered.Addrs[0].String() != direct.String() {
		t.Errorf("expected %s, got %s", direct, filtered.Addrs[0])
	}
}

func TestFilterRelay(t *testing.T) {
	direct, _ := ma.NewMultiaddr("/ip4/1.2.3.4/udp/4001/quic-v1")
	relay, _ := ma.NewMultiaddr("/ip4/5.6.7.8/udp/4001/quic-v1/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN/p2p-circuit")

	pi := peer.AddrInfo{
		ID:    "test-peer",
		Addrs: []ma.Multiaddr{direct, relay},
	}

	filtered := filterRelay(pi)
	if len(filtered.Addrs) != 1 {
		t.Fatalf("expected 1 relay addr, got %d", len(filtered.Addrs))
	}
	if filtered.Addrs[0].String() != relay.String() {
		t.Errorf("expected %s, got %s", relay, filtered.Addrs[0])
	}
}

func TestNATTraverseFromSkipsDirect(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	hostA, err := NewHost(HostConfig{Keypair: kpA, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostA.Close()

	hostB, err := NewHost(HostConfig{Keypair: kpB, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Starting from StrategyHolePunch should skip direct and eventually fall to TorOnly
	// since there's no relay for hole punch to work.
	cfg := NATConfig{
		DirectTimeout:    500 * time.Millisecond,
		HolePunchTimeout: 500 * time.Millisecond,
		RelayTimeout:     500 * time.Millisecond,
	}

	result := NATTraverseFrom(ctx, hostA, hostB.AddrInfo(), cfg, StrategyHolePunch)

	// With only direct addrs (no relay), starting from HolePunch means no relay
	// addrs to try, so it falls to TorOnly.
	if result.Strategy != StrategyTorOnly {
		t.Fatalf("expected tor-only when skipping direct, got %v", result.Strategy)
	}
}

func TestNATTraverseFromDirectStillWorks(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	hostA, err := NewHost(HostConfig{Keypair: kpA, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostA.Close()

	hostB, err := NewHost(HostConfig{Keypair: kpB, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Starting from StrategyDirect should work normally.
	result := NATTraverseFrom(ctx, hostA, hostB.AddrInfo(), DefaultNATConfig(), StrategyDirect)

	if result.Strategy != StrategyDirect {
		t.Fatalf("expected direct, got %v", result.Strategy)
	}
}

func TestReconnectWithFallbackSuccess(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	bobAddr := string(identity.DeriveAddress(bob.PublicKey()))

	hostA, err := NewHost(HostConfig{Keypair: alice, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostA.Close()

	hostB, err := NewHost(HostConfig{Keypair: bob, ListenPort: 0, DisableNATMap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hostB.Close()

	sigListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sigListener.Close()

	nc := signaling.NewNonceCache()

	go func() {
		for {
			conn, err := sigListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				env, _ := protocol.ReadMsg(c)
				req, _ := signaling.ValidateConnRequest(env.Payload, nc)
				respData, _, _ := signaling.CreateConnResponse(true, bob, req.Nonce, "")
				protocol.WriteMsg(c, &pb.Envelope{Type: pb.MessageType_CONN_RESPONSE, Payload: respData})
				protocol.ReadMsg(c)
				bobInfo := &signaling.IPInfo{Addrs: hostB.Addrs()}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				signaling.ExchangeIPs(ctx, c, bobInfo, bob, alice.PublicKey())
			}(conn)
		}
	}()

	connector := NewConnector(ConnectorConfig{
		Keypair: alice,
		Host:    hostA,
		Finder: &mockFinder{
			result: PeerLookupResult{Address: bobAddr, OnionAddr: sigListener.Addr().String()},
		},
		Dialer: &mockTorDialer{
			dial: func(ctx context.Context, addr string) (net.Conn, error) {
				return net.Dial("tcp", addr)
			},
		},
		NATCfg: NATConfig{
			DirectTimeout:    5 * time.Second,
			HolePunchTimeout: 500 * time.Millisecond,
			RelayTimeout:     500 * time.Millisecond,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// First connect normally.
	_, err = connector.Connect(ctx, bobAddr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Simulate disconnect.
	connector.mu.Lock()
	if pc, ok := connector.peers[bobAddr]; ok {
		pc.State = StateFailed
	}
	connector.mu.Unlock()
	hostA.Inner().Network().ClosePeer(hostB.ID())

	// ReconnectWithFallback should succeed.
	pc, err := connector.ReconnectWithFallback(ctx, bobAddr)
	if err != nil {
		t.Fatalf("ReconnectWithFallback: %v", err)
	}
	if pc.State != StateConnected {
		t.Errorf("expected connected, got %v", pc.State)
	}
}
