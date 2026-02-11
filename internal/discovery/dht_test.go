package discovery

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

// testNode bundles a DHT node with its listener for testing.
type testNode struct {
	dht      *DHTDiscovery
	peer     *PeerInfo
	listener net.Listener
	addr     string // local TCP address
}

// testDialer maps onion addresses to local TCP addresses for in-memory testing.
type testDialer struct {
	mu    sync.RWMutex
	addrs map[string]string
}

func newTestDialer() *testDialer {
	return &testDialer{addrs: make(map[string]string)}
}

func (d *testDialer) Register(onionAddr, localAddr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addrs[onionAddr] = localAddr
}

func (d *testDialer) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	d.mu.RLock()
	localAddr, ok := d.addrs[addr]
	d.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown address: %s", addr)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", localAddr)
}

// createTestNode creates a DHT node with a local TCP listener.
func createTestNode(t *testing.T, dialer *testDialer) *testNode {
	t.Helper()

	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	addr := identity.DeriveAddress(kp.PublicKey())
	nodeID := NodeIDFromPublicKey(kp.PublicKey())
	onionAddr := fmt.Sprintf("%s.onion", addr.Short())

	peer := &PeerInfo{
		ID:        nodeID,
		Address:   addr.String(),
		OnionAddr: onionAddr,
		LastSeen:  time.Now(),
	}

	rt := NewRoutingTable(nodeID)
	dht := NewDHTDiscovery(rt, peer, dialer)

	// Start local TCP listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	localAddr := ln.Addr().String()

	// Register onion → local address mapping.
	dialer.Register(onionAddr, localAddr)

	// Accept connections in background.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go dht.HandleConn(conn)
		}
	}()

	t.Cleanup(func() { ln.Close() })

	return &testNode{
		dht:      dht,
		peer:     peer,
		listener: ln,
		addr:     localAddr,
	}
}

func TestDHT_AnnounceAndLookup(t *testing.T) {
	dialer := newTestDialer()

	// Create two nodes: A and B.
	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)

	// Manually add B to A's routing table so A knows about B.
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)

	// A announces itself to B.
	ctx := context.Background()
	if err := nodeA.dht.Announce(ctx); err != nil {
		t.Fatalf("Announce: %v", err)
	}

	// Give a moment for the record to propagate.
	time.Sleep(50 * time.Millisecond)

	// B should now have A in its routing table.
	if nodeB.dht.rt.Size() == 0 {
		t.Fatal("B should have received A's announcement")
	}

	// B looks up A by address.
	found, err := nodeB.dht.Lookup(ctx, nodeA.peer.Address)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found.Address != nodeA.peer.Address {
		t.Fatalf("expected address %s, got %s", nodeA.peer.Address, found.Address)
	}
	if found.OnionAddr != nodeA.peer.OnionAddr {
		t.Fatalf("expected onion %s, got %s", nodeA.peer.OnionAddr, found.OnionAddr)
	}
}

func TestDHT_LookupViaIntermediary(t *testing.T) {
	dialer := newTestDialer()

	// Three nodes: A, B, C.
	// A knows B. B knows C. A looks up C (should find via B).
	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)
	nodeC := createTestNode(t, dialer)

	// Wire up: A knows B, B knows C.
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)
	nodeB.dht.rt.AddPeer(nodeC.peer, nodeC.peer.ID)

	// C announces to B.
	ctx := context.Background()
	nodeC.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)
	if err := nodeC.dht.Announce(ctx); err != nil {
		t.Fatalf("C announce: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// A looks up C by address — should find via B.
	found, err := nodeA.dht.Lookup(ctx, nodeC.peer.Address)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found.Address != nodeC.peer.Address {
		t.Fatalf("expected %s, got %s", nodeC.peer.Address, found.Address)
	}
}

func TestDHT_ExpiredRecordRejected(t *testing.T) {
	dialer := newTestDialer()

	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)

	// Add B to A's routing table.
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)

	// Create an expired peer and manually add it to B.
	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey())
	expiredPeer := &PeerInfo{
		ID:        NodeIDFromPublicKey(kp.PublicKey()),
		Address:   addr.String(),
		OnionAddr: "expired.onion",
		LastSeen:  time.Now().Add(-25 * time.Hour), // >24h ago
	}
	nodeB.dht.rt.AddPeer(expiredPeer, expiredPeer.ID)

	// A tries to look up the expired peer — should not find it.
	ctx := context.Background()
	_, err := nodeA.dht.Lookup(ctx, expiredPeer.Address)
	if err == nil {
		t.Fatal("expected error for expired record")
	}
}

func TestDHT_LookupNotFound(t *testing.T) {
	dialer := newTestDialer()

	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)

	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)

	// Look up a nonexistent address.
	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey())

	ctx := context.Background()
	_, err := nodeA.dht.Lookup(ctx, addr.String())
	if err == nil {
		t.Fatal("expected error for nonexistent peer")
	}
}

func TestDHT_AnnounceNopeers(t *testing.T) {
	dialer := newTestDialer()
	node := createTestNode(t, dialer)

	ctx := context.Background()
	err := node.dht.Announce(ctx)
	if err == nil {
		t.Fatal("expected error when no peers in routing table")
	}
}

func TestDHT_HandlePutRecordStoresRecord(t *testing.T) {
	dialer := newTestDialer()

	nodeA := createTestNode(t, dialer)
	nodeB := createTestNode(t, dialer)

	// A knows B.
	nodeA.dht.rt.AddPeer(nodeB.peer, nodeB.peer.ID)

	// A announces.
	ctx := context.Background()
	if err := nodeA.dht.Announce(ctx); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify B stored A's record.
	closest := nodeB.dht.rt.FindClosest(nodeA.peer.ID, 1)
	if len(closest) == 0 {
		t.Fatal("B should have stored A's record")
	}
	if closest[0].Address != nodeA.peer.Address {
		t.Fatalf("B stored wrong address: %s", closest[0].Address)
	}
}

func TestRecordToPeerInfo_Nil(t *testing.T) {
	if recordToPeerInfo(nil) != nil {
		t.Fatal("nil record should return nil")
	}
}

func TestRecordToPeerInfo_ShortNodeID(t *testing.T) {
	r := &pb.PeerRecord{NodeId: []byte{1, 2, 3}} // too short
	if recordToPeerInfo(r) != nil {
		t.Fatal("short node ID should return nil")
	}
}
