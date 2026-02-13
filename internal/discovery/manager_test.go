package discovery

import (
	"context"
	"os"
	"path/filepath"
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

func TestManager_RTSize(t *testing.T) {
	dialer := newTestDialer()
	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)

	// Add B to A's routing table.
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)

	mgr := NewManager(nodeA.dht, nil)
	if mgr.RTSize() != 1 {
		t.Fatalf("expected RTSize 1, got %d", mgr.RTSize())
	}
}

func TestManager_RTSize_NilDHT(t *testing.T) {
	mgr := NewManager(nil, nil)
	if mgr.RTSize() != 0 {
		t.Fatalf("expected RTSize 0 with nil DHT, got %d", mgr.RTSize())
	}
}

func TestManager_ListPeers(t *testing.T) {
	dialer := newTestDialer()
	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)
	nodeC := createTestNode(t, dialer)

	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)
	nodeA.dht.rt.AddPeer(nodeC.peer, nodeC.peer.ID)

	mgr := NewManager(nodeA.dht, nil)
	peers := mgr.ListPeers()
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}

	addrs := map[string]bool{}
	for _, p := range peers {
		addrs[p.Address] = true
	}
	if !addrs[nodeB.peer.Address] || !addrs[nodeC.peer.Address] {
		t.Fatal("ListPeers should return all peers from routing table")
	}
}

func TestManager_ListPeers_NilDHT(t *testing.T) {
	mgr := NewManager(nil, nil)
	peers := mgr.ListPeers()
	if peers != nil {
		t.Fatal("expected nil peers with nil DHT")
	}
}

func TestManager_SetRTPath(t *testing.T) {
	mgr := NewManager(nil, nil)
	mgr.SetRTPath("/tmp/test_rt.json")

	mgr.mu.Lock()
	path := mgr.rtPath
	mgr.mu.Unlock()

	if path != "/tmp/test_rt.json" {
		t.Fatalf("expected /tmp/test_rt.json, got %s", path)
	}
}

func TestManager_AddNode_AnnouncesAndSaves(t *testing.T) {
	dialer := newTestDialer()

	// Create a seed with some peers.
	seed := createTestNode(t, dialer)
	peerB := createTestNode(t, dialer)
	seed.dht.rt.AddPeer(peerB.peer, peerB.peer.ID)

	// Create bootstrapper with a manager.
	bootstrapper := createTestNode(t, dialer)
	mgr := NewManager(bootstrapper.dht, nil)

	// Set RT path so save happens.
	rtPath := filepath.Join(t.TempDir(), "routing.json")
	mgr.SetRTPath(rtPath)

	ctx := context.Background()
	n, err := mgr.AddNode(ctx, seed.peer.OnionAddr)
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if n == 0 {
		t.Fatal("expected peers from bootstrap")
	}

	// Verify routing table was saved to disk.
	if _, statErr := os.Stat(rtPath); os.IsNotExist(statErr) {
		t.Fatal("routing table file should have been saved after AddNode")
	}

	// Wait briefly for announce to propagate.
	time.Sleep(100 * time.Millisecond)

	// Verify peerB received our announcement (bootstrapper is now known to peerB).
	found := false
	for _, p := range peerB.dht.rt.AllPeers() {
		if p.Address == bootstrapper.peer.Address {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected peerB to know about bootstrapper after announce")
	}
}

func TestManager_StartAnnounceLoop_Cancellation(t *testing.T) {
	dialer := newTestDialer()
	nodeA := createTestNode(t, dialer)

	mgr := NewManager(nodeA.dht, nil)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.StartAnnounceLoop(ctx)

	// Cancel immediately — should not panic or hang.
	cancel()
	time.Sleep(50 * time.Millisecond)
}
