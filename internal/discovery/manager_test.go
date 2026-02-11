package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestManager_FindPeer_DHTFallback(t *testing.T) {
	dialer := newTestDialer()

	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)

	// A knows B, B has announced.
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)
	nodeB.dht.rt.AddPeer(nodeA.peer, nodeA.peer.ID)

	ctx := context.Background()
	nodeB.dht.Announce(ctx)
	time.Sleep(50 * time.Millisecond)

	// Manager with DHT only (no mDNS).
	mgr := NewManager(nodeA.dht, nil)

	found, err := mgr.FindPeer(ctx, nodeB.peer.Address)
	if err != nil {
		t.Fatalf("FindPeer: %v", err)
	}
	if found.Address != nodeB.peer.Address {
		t.Fatalf("expected %s, got %s", nodeB.peer.Address, found.Address)
	}
}

func TestManager_FindPeer_NotFound(t *testing.T) {
	dialer := newTestDialer()

	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)

	mgr := NewManager(nodeA.dht, nil)

	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey()).String()

	ctx := context.Background()
	_, err := mgr.FindPeer(ctx, addr)
	if err == nil {
		t.Fatal("expected error for nonexistent peer")
	}
}

func TestManager_PeerChan_Dedup(t *testing.T) {
	mgr := NewManager(nil, nil)

	peer := generateTestPeer(t)

	// Emit same peer twice.
	mgr.emitIfNew(peer)
	mgr.emitIfNew(peer)

	// Should only receive one.
	select {
	case p := <-mgr.PeerChan():
		if p.Address != peer.Address {
			t.Fatalf("wrong peer address: %s", p.Address)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected peer on channel")
	}

	// Second read should timeout — duplicate was filtered.
	select {
	case <-mgr.PeerChan():
		t.Fatal("should not receive duplicate")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestManager_PeerChan_MultiplePeers(t *testing.T) {
	mgr := NewManager(nil, nil)

	p1 := generateTestPeer(t)
	p2 := generateTestPeer(t)

	mgr.emitIfNew(p1)
	mgr.emitIfNew(p2)

	received := make(map[string]bool)
	for range 2 {
		select {
		case p := <-mgr.PeerChan():
			received[p.Address] = true
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected peer on channel")
		}
	}

	if !received[p1.Address] || !received[p2.Address] {
		t.Fatal("should receive both distinct peers")
	}
}

func TestManager_NoBackends(t *testing.T) {
	mgr := NewManager(nil, nil)

	ctx := context.Background()
	_, err := mgr.FindPeer(ctx, "EnonexistentAddr")
	if err == nil {
		t.Fatal("expected error with no backends")
	}
}
