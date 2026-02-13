package node

import (
	"testing"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestNode_RTSize_NoDiscovery(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)

	// No discovery set — should return 0.
	if got := n.RTSize(); got != 0 {
		t.Fatalf("RTSize with no discovery: got %d, want 0", got)
	}
}

func TestNode_GetDebugInfo_NoSubsystems(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)

	info := n.GetDebugInfo()
	if info == nil {
		t.Fatal("GetDebugInfo should never return nil")
	}
	if info.RTSize != 0 {
		t.Fatalf("RTSize: got %d, want 0", info.RTSize)
	}
	if len(info.RTPeers) != 0 {
		t.Fatalf("RTPeers: got %d, want 0", len(info.RTPeers))
	}
	if len(info.Connections) != 0 {
		t.Fatalf("Connections: got %d, want 0", len(info.Connections))
	}
	if info.OnionAddr != "" {
		t.Fatalf("OnionAddr: got %q, want empty", info.OnionAddr)
	}
}

func TestNode_GetDebugInfo_WithOnionAddr(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)
	n.SetOnionAddr("testaddr.onion")

	info := n.GetDebugInfo()
	if info.OnionAddr != "testaddr.onion" {
		t.Fatalf("OnionAddr: got %q, want %q", info.OnionAddr, "testaddr.onion")
	}
}

func TestNode_Address(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)

	addr := n.Address()
	if addr.String() == "" {
		t.Fatal("address should not be empty")
	}
	if addr.String()[0] != 'E' {
		t.Fatalf("address should start with E, got %q", addr.String())
	}
}

func TestNode_TorStateDefault(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)

	if got := n.TorState(); got != "disabled" {
		t.Fatalf("default TorState: got %q, want %q", got, "disabled")
	}
}

func TestNode_SetTorState(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)

	n.SetTorState("ready")
	if got := n.TorState(); got != "ready" {
		t.Fatalf("TorState after set: got %q, want %q", got, "ready")
	}
}

func TestNode_PeerCount_NoConnector(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	n := New(kp)

	if got := n.PeerCount(); got != 0 {
		t.Fatalf("PeerCount with no connector: got %d, want 0", got)
	}
}
