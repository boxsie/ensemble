package transport

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestPingPong(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	hA, err := NewHost(HostConfig{Keypair: kpA})
	if err != nil {
		t.Fatalf("creating host A: %v", err)
	}
	defer hA.Close()

	hB, err := NewHost(HostConfig{Keypair: kpB})
	if err != nil {
		t.Fatalf("creating host B: %v", err)
	}
	defer hB.Close()

	presA := NewPresence(hA, kpA)
	defer presA.Close()
	presB := NewPresence(hB, kpB)
	defer presB.Close()

	// Connect the two hosts.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := hA.Connect(ctx, hB.AddrInfo()); err != nil {
		t.Fatalf("connecting: %v", err)
	}

	// Ping B from A.
	rtt, err := presA.PingPeer(ctx, hB.ID())
	if err != nil {
		t.Fatalf("PingPeer() error: %v", err)
	}
	if rtt <= 0 {
		t.Fatalf("RTT should be positive, got %v", rtt)
	}
}

func TestPingUnconnectedPeer(t *testing.T) {
	kp, _ := identity.Generate()
	h, err := NewHost(HostConfig{Keypair: kp})
	if err != nil {
		t.Fatalf("creating host: %v", err)
	}
	defer h.Close()

	pres := NewPresence(h, kp)
	defer pres.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = pres.PingPeer(ctx, "invalid-peer")
	if err == nil {
		t.Fatal("expected error for unconnected peer")
	}
}

type mockPeerLister struct {
	peers []*PeerConnection
}

func (m *mockPeerLister) ActivePeers() []*PeerConnection {
	return m.peers
}

func TestPresenceOnlineCallback(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()
	addrB := identity.DeriveAddress(kpB.PublicKey()).String()

	hA, err := NewHost(HostConfig{Keypair: kpA})
	if err != nil {
		t.Fatalf("creating host A: %v", err)
	}
	defer hA.Close()

	hB, err := NewHost(HostConfig{Keypair: kpB})
	if err != nil {
		t.Fatalf("creating host B: %v", err)
	}
	defer hB.Close()

	presA := NewPresence(hA, kpA)
	defer presA.Close()
	_ = NewPresence(hB, kpB) // B must have handler registered

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := hA.Connect(ctx, hB.AddrInfo()); err != nil {
		t.Fatalf("connecting: %v", err)
	}

	lister := &mockPeerLister{
		peers: []*PeerConnection{
			{Address: addrB, PeerID: hB.ID()},
		},
	}

	var mu sync.Mutex
	var statuses []OnlineStatus

	presA.pingAll(ctx, lister, func(s OnlineStatus) {
		mu.Lock()
		statuses = append(statuses, s)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()

	// First ping should report online (was previously unknown/offline).
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status change, got %d", len(statuses))
	}
	if !statuses[0].Online {
		t.Fatal("peer should be online")
	}
	if statuses[0].Address != addrB {
		t.Fatalf("address: got %q, want %q", statuses[0].Address, addrB)
	}

	if !presA.IsOnline(addrB) {
		t.Fatal("IsOnline should return true after successful ping")
	}
}

func TestIsOnlineDefaultFalse(t *testing.T) {
	kp, _ := identity.Generate()
	h, err := NewHost(HostConfig{Keypair: kp})
	if err != nil {
		t.Fatalf("creating host: %v", err)
	}
	defer h.Close()

	pres := NewPresence(h, kp)
	defer pres.Close()

	if pres.IsOnline("EsomeAddr") {
		t.Fatal("should default to offline")
	}
}
