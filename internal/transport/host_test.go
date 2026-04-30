package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestHostStartsAndListens(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	h, err := NewHost(HostConfig{
		Keypair:       kp,
		ListenPort:    0,
		DisableNATMap: true,
	})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer h.Close()

	if h.ID() == "" {
		t.Fatal("expected non-empty peer ID")
	}

	addrs := h.Addrs()
	if len(addrs) == 0 {
		t.Fatal("expected at least one listen address")
	}

	hasQUIC := false
	for _, a := range addrs {
		if strings.Contains(a, "quic-v1") {
			hasQUIC = true
			break
		}
	}
	if !hasQUIC {
		t.Errorf("expected a quic-v1 address, got %v", addrs)
	}
}

func TestTwoHostsConnect(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	hostA, err := NewHost(HostConfig{
		Keypair:       kpA,
		ListenPort:    0,
		DisableNATMap: true,
	})
	if err != nil {
		t.Fatalf("host A: %v", err)
	}
	defer hostA.Close()

	hostB, err := NewHost(HostConfig{
		Keypair:       kpB,
		ListenPort:    0,
		DisableNATMap: true,
	})
	if err != nil {
		t.Fatalf("host B: %v", err)
	}
	defer hostB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := hostA.Connect(ctx, hostB.AddrInfo()); err != nil {
		t.Fatalf("A→B connect: %v", err)
	}

	// Verify B sees the connection from A.
	conns := hostB.Inner().Network().ConnsToPeer(hostA.ID())
	if len(conns) == 0 {
		t.Fatal("B has no connections from A")
	}
}

func TestTwoHostsStream(t *testing.T) {
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	hostA, err := NewHost(HostConfig{
		Keypair:       kpA,
		ListenPort:    0,
		DisableNATMap: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hostA.Close()

	hostB, err := NewHost(HostConfig{
		Keypair:       kpB,
		ListenPort:    0,
		DisableNATMap: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hostB.Close()

	const testProto = protocol.ID("/test/1.0.0")
	done := make(chan string, 1)

	hostB.Inner().SetStreamHandler(testProto, func(s network.Stream) {
		defer s.Close()
		buf := make([]byte, 64)
		n, _ := s.Read(buf)
		done <- string(buf[:n])
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := hostA.Connect(ctx, hostB.AddrInfo()); err != nil {
		t.Fatal(err)
	}

	s, err := hostA.Inner().NewStream(ctx, hostB.ID(), testProto)
	if err != nil {
		t.Fatalf("opening stream: %v", err)
	}
	msg := "hello from A"
	s.Write([]byte(msg))
	s.Close()

	select {
	case got := <-done:
		if got != msg {
			t.Errorf("got %q, want %q", got, msg)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for stream data")
	}
}

func TestHostClose(t *testing.T) {
	kp, _ := identity.Generate()
	h, err := NewHost(HostConfig{
		Keypair:       kp,
		ListenPort:    0,
		DisableNATMap: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// After close, no listen addrs should remain.
	if len(h.Inner().Network().ListenAddresses()) != 0 {
		t.Error("expected no listen addresses after close")
	}
}
