package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/daemon"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/transport"
)

// --- mock node for DirectBackend tests ---

type mockNode struct {
	kp   *identity.Keypair
	addr identity.Address
	bus  *node.EventBus
}

func (m *mockNode) Identity() *identity.Keypair                            { return m.kp }
func (m *mockNode) Address() identity.Address                               { return m.addr }
func (m *mockNode) EventBus() *node.EventBus                                { return m.bus }
func (m *mockNode) TorState() string                                        { return "disabled" }
func (m *mockNode) OnionAddr() string                                       { return "" }
func (m *mockNode) PeerCount() int32                                        { return 0 }
func (m *mockNode) Contacts() *contacts.Store                               { return nil }
func (m *mockNode) AcceptConnection(_ string) error                         { return fmt.Errorf("not implemented") }
func (m *mockNode) RejectConnection(_, _ string) error                      { return fmt.Errorf("not implemented") }
func (m *mockNode) Connect(_ context.Context, _ string) (*signaling.HandshakeResult, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockNode) ConnectionInfo(_ string) *transport.PeerConnection { return nil }
func (m *mockNode) ActiveConnections() []*transport.PeerConnection    { return nil }
func (m *mockNode) SendMessage(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (m *mockNode) SendFile(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (m *mockNode) AcceptFile(_, _ string) error  { return fmt.Errorf("not implemented") }
func (m *mockNode) RejectFile(_, _ string) error  { return fmt.Errorf("not implemented") }
func (m *mockNode) AddNode(_ context.Context, _ string) (int, error) { return 0, fmt.Errorf("not implemented") }

func newMockNode(t *testing.T) *mockNode {
	t.Helper()
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	return &mockNode{
		kp:   kp,
		addr: identity.DeriveAddress(kp.PublicKey()),
		bus:  node.NewEventBus(),
	}
}

// --- DirectBackend tests ---

func TestDirectGetIdentity(t *testing.T) {
	mn := newMockNode(t)
	b := NewDirectBackend(mn)

	info, err := b.GetIdentity(context.Background())
	if err != nil {
		t.Fatalf("GetIdentity() error: %v", err)
	}

	if info.Address != mn.addr.String() {
		t.Fatalf("address: got %q, want %q", info.Address, mn.addr.String())
	}
	if len(info.PublicKey) != 32 {
		t.Fatalf("public key size: got %d, want 32", len(info.PublicKey))
	}
}

func TestDirectGetStatus(t *testing.T) {
	mn := newMockNode(t)
	b := NewDirectBackend(mn)

	status, err := b.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus() error: %v", err)
	}

	if status.TorState != "disabled" {
		t.Fatalf("tor state: got %q, want %q", status.TorState, "disabled")
	}
}

func TestDirectSubscribe(t *testing.T) {
	mn := newMockNode(t)
	b := NewDirectBackend(mn)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}

	mn.bus.Publish(node.Event{Type: node.EventTorReady, Payload: "ready"})

	select {
	case e := <-ch:
		if e.Type != node.EventTorReady {
			t.Fatalf("event type: got %d, want %d", e.Type, node.EventTorReady)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
}

func TestDirectClose(t *testing.T) {
	mn := newMockNode(t)
	b := NewDirectBackend(mn)

	if err := b.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// --- GRPCBackend tests (end-to-end via daemon) ---

func TestGRPCGetIdentity(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	d := daemon.New(daemon.Config{
		DataDir:    dir,
		SocketPath: sock,
		DisableTor: true,
		DisableP2P: true,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon Start() error: %v", err)
	}
	defer d.Stop()

	b, err := NewGRPCBackend("unix://" + sock)
	if err != nil {
		t.Fatalf("NewGRPCBackend() error: %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := b.GetIdentity(ctx)
	if err != nil {
		t.Fatalf("GetIdentity() error: %v", err)
	}

	if info.Address == "" {
		t.Fatal("address should not be empty")
	}
	if info.Address[0] != 'E' {
		t.Fatalf("address should start with E, got %q", info.Address)
	}
	if len(info.PublicKey) != 32 {
		t.Fatalf("public key size: got %d, want 32", len(info.PublicKey))
	}
}

func TestGRPCGetStatus(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	d := daemon.New(daemon.Config{
		DataDir:    dir,
		SocketPath: sock,
		DisableTor: true,
		DisableP2P: true,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon Start() error: %v", err)
	}
	defer d.Stop()

	b, err := NewGRPCBackend("unix://" + sock)
	if err != nil {
		t.Fatalf("NewGRPCBackend() error: %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := b.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus() error: %v", err)
	}

	if status.TorState != "disabled" {
		t.Fatalf("tor state: got %q, want %q", status.TorState, "disabled")
	}
}

func TestGRPCSubscribe(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")

	d := daemon.New(daemon.Config{
		DataDir:    dir,
		SocketPath: sock,
		DisableTor: true,
		DisableP2P: true,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("daemon Start() error: %v", err)
	}
	defer d.Stop()

	b, err := NewGRPCBackend("unix://" + sock)
	if err != nil {
		t.Fatalf("NewGRPCBackend() error: %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}

	// Give the server-side stream time to set up event bus subscriptions.
	time.Sleep(100 * time.Millisecond)

	// Publish via the daemon's node event bus.
	d.Node().EventBus().Publish(node.Event{Type: node.EventPeerDiscovered, Payload: "test-peer"})

	select {
	case e := <-ch:
		if e.Type != node.EventPeerDiscovered {
			t.Fatalf("event type: got %d, want %d", e.Type, node.EventPeerDiscovered)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
}
