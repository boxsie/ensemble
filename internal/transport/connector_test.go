package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/signaling"
)

// --- mock peer finder ---

type mockFinder struct {
	result PeerLookupResult
	err    error
}

func (m *mockFinder) FindPeer(_ context.Context, _ string) (PeerLookupResult, error) {
	return m.result, m.err
}

// --- mock dialer that connects to a local signaling server ---

type mockTorDialer struct {
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

func (d *mockTorDialer) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	return d.dial(ctx, addr)
}

func TestConnectorFullFlow(t *testing.T) {
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	bobAddr := string(identity.DeriveAddress(bob.PublicKey()))

	// Create libp2p hosts for direct connection.
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

	// Set up a mock signaling server for Bob (accepts connections, does IP exchange).
	sigListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sigListener.Close()

	nc := signaling.NewNonceCache()

	// Run Bob's signaling responder in a goroutine.
	go func() {
		conn, err := sigListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read ConnRequest (Step 1).
		env, _ := protocol.ReadMsg(conn)
		req, _ := signaling.ValidateConnRequest(env.Payload, nc)

		// Send ConnResponse (Step 2) — accept.
		respData, _, _ := signaling.CreateConnResponse(true, bob, req.Nonce, "")
		protocol.WriteMsg(conn, &pb.Envelope{Type: pb.MessageType_CONN_RESPONSE, Payload: respData})

		// Read ConnConfirm (Step 3).
		protocol.ReadMsg(conn)

		// IP Exchange — Bob sends his host addrs.
		bobInfo := &signaling.IPInfo{Addrs: hostB.Addrs()}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		signaling.ExchangeIPs(ctx, conn, bobInfo, bob, alice.PublicKey())
	}()

	// Track state transitions.
	var states []ConnState
	var mu sync.Mutex

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
		OnState: func(e StateEvent) {
			mu.Lock()
			states = append(states, e.State)
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pc, err := connector.Connect(ctx, bobAddr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if pc.State != StateConnected {
		t.Errorf("expected StateConnected, got %v", pc.State)
	}
	if pc.Strategy != StrategyDirect {
		t.Errorf("expected StrategyDirect, got %v", pc.Strategy)
	}
	if pc.PeerID == "" {
		t.Error("expected non-empty peer ID")
	}

	// Verify state transitions.
	mu.Lock()
	defer mu.Unlock()

	expectedStates := []ConnState{
		StateDiscovering,
		StateSignaling,
		StateExchangingIPs,
		StateNATTraversal,
		StateConnected,
	}
	if len(states) != len(expectedStates) {
		t.Fatalf("state transitions: got %v, want %v", states, expectedStates)
	}
	for i, s := range states {
		if s != expectedStates[i] {
			t.Errorf("state[%d]: got %v, want %v", i, s, expectedStates[i])
		}
	}
}

func TestConnectorDiscoveryFails(t *testing.T) {
	kp, _ := identity.Generate()
	h, _ := NewHost(HostConfig{Keypair: kp, ListenPort: 0, DisableNATMap: true})
	defer h.Close()

	var states []ConnState
	var mu sync.Mutex

	connector := NewConnector(ConnectorConfig{
		Keypair: kp,
		Host:    h,
		Finder: &mockFinder{
			err: fmt.Errorf("peer not found"),
		},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("should not be called")
			},
		},
		OnState: func(e StateEvent) {
			mu.Lock()
			states = append(states, e.State)
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := connector.Connect(ctx, "EsomeFakeAddress")
	if err == nil {
		t.Fatal("expected error")
	}

	mu.Lock()
	defer mu.Unlock()
	// Should see: Discovering → Failed.
	if len(states) < 2 || states[len(states)-1] != StateFailed {
		t.Errorf("expected StateFailed, got %v", states)
	}
}

func TestConnectorDisconnect(t *testing.T) {
	kp, _ := identity.Generate()
	h, _ := NewHost(HostConfig{Keypair: kp, ListenPort: 0, DisableNATMap: true})
	defer h.Close()

	connector := NewConnector(ConnectorConfig{
		Keypair: kp,
		Host:    h,
		Finder:  &mockFinder{},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("unused")
			},
		},
	})

	// Manually insert a peer connection.
	connector.mu.Lock()
	connector.peers["test-addr"] = &PeerConnection{Address: "test-addr", State: StateConnected}
	connector.mu.Unlock()

	connector.Disconnect("test-addr")

	if pc := connector.GetPeer("test-addr"); pc != nil {
		t.Errorf("expected nil after disconnect, got %v", pc)
	}
}

func TestConnStateString(t *testing.T) {
	tests := []struct {
		s    ConnState
		want string
	}{
		{StateDiscovering, "discovering"},
		{StateSignaling, "signaling"},
		{StateExchangingIPs, "exchanging-ips"},
		{StateNATTraversal, "nat-traversal"},
		{StateConnected, "connected"},
		{StateConnectedTor, "connected-tor"},
		{StateDisconnected, "disconnected"},
		{StateFailed, "failed"},
		{ConnState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("ConnState(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestConnectorGetPeerNotFound(t *testing.T) {
	kp, _ := identity.Generate()
	connector := NewConnector(ConnectorConfig{
		Keypair: kp,
		Finder:  &mockFinder{},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("unused")
			},
		},
	})
	if pc := connector.GetPeer("nonexistent"); pc != nil {
		t.Errorf("expected nil, got %v", pc)
	}
}

func TestReconnectStateString(t *testing.T) {
	if got := StateReconnecting.String(); got != "reconnecting" {
		t.Errorf("StateReconnecting.String() = %q, want %q", got, "reconnecting")
	}
}

// --- mock finder with call counting ---

type countingFinder struct {
	result PeerLookupResult
	err    error
	mu     sync.Mutex
	calls  int
}

func (f *countingFinder) FindPeer(_ context.Context, _ string) (PeerLookupResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.result, f.err
}

func (f *countingFinder) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestReconnectWithRetry(t *testing.T) {
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

	// Accept connections in a loop (reconnection needs multiple accepts).
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

	var states []ConnState
	var mu sync.Mutex

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
		OnState: func(e StateEvent) {
			mu.Lock()
			states = append(states, e.State)
			mu.Unlock()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pc, err := connector.Reconnect(ctx, bobAddr)
	if err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	if pc.State != StateConnected {
		t.Errorf("expected StateConnected, got %v", pc.State)
	}

	// Should see StateReconnecting in the state trace.
	mu.Lock()
	hasReconnecting := false
	for _, s := range states {
		if s == StateReconnecting {
			hasReconnecting = true
			break
		}
	}
	mu.Unlock()

	if !hasReconnecting {
		t.Error("expected StateReconnecting in state transitions")
	}
}

func TestReconnectUsesCache(t *testing.T) {
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

	finder := &countingFinder{
		result: PeerLookupResult{Address: bobAddr, OnionAddr: sigListener.Addr().String()},
	}

	connector := NewConnector(ConnectorConfig{
		Keypair: alice,
		Host:    hostA,
		Finder:  finder,
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

	// First connect populates the cache.
	_, err = connector.Connect(ctx, bobAddr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	firstCalls := finder.CallCount()

	// Simulate disconnect by removing peer and clearing libp2p state.
	connector.mu.Lock()
	delete(connector.peers, bobAddr)
	connector.mu.Unlock()
	hostA.Inner().Network().ClosePeer(hostB.ID())

	// Reconnect should use cached lookup (no additional FindPeer call).
	_, err = connector.Reconnect(ctx, bobAddr)
	if err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	secondCalls := finder.CallCount()
	if secondCalls != firstCalls {
		t.Errorf("expected cached lookup (calls stayed at %d), but got %d", firstCalls, secondCalls)
	}
}

func TestReconnectAllRetriesFail(t *testing.T) {
	kp, _ := identity.Generate()

	connector := NewConnector(ConnectorConfig{
		Keypair: kp,
		Finder: &mockFinder{
			err: fmt.Errorf("peer unreachable"),
		},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("should not be called")
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := connector.Reconnect(ctx, "EsomeTestAddress")
	if err == nil {
		t.Fatal("expected error when all retries fail")
	}

	// Should see StateFailed.
	pc := connector.GetPeer("EsomeTestAddress")
	if pc == nil {
		t.Fatal("expected peer entry to exist")
	}
	if pc.State != StateFailed {
		t.Errorf("expected StateFailed, got %v", pc.State)
	}
}

func TestEvictLRUPeer(t *testing.T) {
	kp, _ := identity.Generate()

	connector := NewConnector(ConnectorConfig{
		Keypair:  kp,
		MaxPeers: 2,
		Finder:   &mockFinder{},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("unused")
			},
		},
	})

	// Manually add 2 peers.
	now := time.Now()
	connector.mu.Lock()
	connector.peers["peer-A"] = &PeerConnection{
		Address:      "peer-A",
		State:        StateConnected,
		LastActivity: now.Add(-10 * time.Minute), // oldest
	}
	connector.peers["peer-B"] = &PeerConnection{
		Address:      "peer-B",
		State:        StateConnected,
		LastActivity: now.Add(-1 * time.Minute),
	}
	connector.mu.Unlock()

	// Now add a third (should evict peer-A, the oldest).
	connector.mu.Lock()
	connector.evictIfNeededLocked()
	connector.peers["peer-C"] = &PeerConnection{
		Address:      "peer-C",
		State:        StateConnected,
		LastActivity: now,
	}
	connector.mu.Unlock()

	if pc := connector.GetPeer("peer-A"); pc != nil {
		t.Error("peer-A should have been evicted (LRU)")
	}
	if pc := connector.GetPeer("peer-B"); pc == nil {
		t.Error("peer-B should still be present")
	}
	if pc := connector.GetPeer("peer-C"); pc == nil {
		t.Error("peer-C should be present")
	}
}

func TestMaxPeersDefault(t *testing.T) {
	kp, _ := identity.Generate()
	connector := NewConnector(ConnectorConfig{
		Keypair: kp,
		Finder:  &mockFinder{},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("unused")
			},
		},
	})
	if connector.MaxPeers() != 50 {
		t.Errorf("expected default MaxPeers=50, got %d", connector.MaxPeers())
	}
	if connector.MaxTransfers() != 5 {
		t.Errorf("expected default MaxTransfers=5, got %d", connector.MaxTransfers())
	}
}

func TestTransferSlotLimiting(t *testing.T) {
	kp, _ := identity.Generate()
	connector := NewConnector(ConnectorConfig{
		Keypair:  kp,
		MaxXfers: 2,
		Finder:   &mockFinder{},
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("unused")
			},
		},
	})

	// Acquire 2 slots.
	if !connector.AcquireTransferSlot() {
		t.Fatal("first slot should succeed")
	}
	if !connector.AcquireTransferSlot() {
		t.Fatal("second slot should succeed")
	}
	if connector.AcquireTransferSlot() {
		t.Fatal("third slot should fail (at limit)")
	}

	if connector.ActiveTransfers() != 2 {
		t.Errorf("expected 2 active, got %d", connector.ActiveTransfers())
	}

	// Release one.
	connector.ReleaseTransferSlot()
	if connector.ActiveTransfers() != 1 {
		t.Errorf("expected 1 active, got %d", connector.ActiveTransfers())
	}

	// Now acquire should succeed again.
	if !connector.AcquireTransferSlot() {
		t.Fatal("slot should be available after release")
	}
}

func TestCachedLookupFallsBackToStale(t *testing.T) {
	kp, _ := identity.Generate()

	// First call succeeds, subsequent calls fail.
	callCount := 0
	failingFinder := &mockTorDialer{} // unused, just need an interface
	_ = failingFinder

	finder := &mockFinder{
		result: PeerLookupResult{Address: "test", OnionAddr: "test.onion"},
	}

	connector := NewConnector(ConnectorConfig{
		Keypair: kp,
		Finder:  finder,
		Dialer: &mockTorDialer{
			dial: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, fmt.Errorf("unused")
			},
		},
	})

	ctx := context.Background()

	// Populate cache.
	result, err := connector.cachedOrFreshLookup(ctx, "peerX")
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if result.OnionAddr != "test.onion" {
		t.Fatalf("expected test.onion, got %s", result.OnionAddr)
	}

	// Expire the cache entry.
	connector.mu.Lock()
	connector.lookupCache["peerX"].CachedAt = time.Now().Add(-2 * CachedLookupMaxAge)
	connector.mu.Unlock()

	// Make finder fail — should fall back to stale cache.
	finder.err = fmt.Errorf("DHT unavailable")

	result, err = connector.cachedOrFreshLookup(ctx, "peerX")
	if err != nil {
		t.Fatalf("stale fallback should not error: %v", err)
	}
	if result.OnionAddr != "test.onion" {
		t.Fatalf("expected stale test.onion, got %s", result.OnionAddr)
	}

	_ = callCount
}
