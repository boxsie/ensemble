package chat

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/services"
	"github.com/boxsie/ensemble/internal/transport"
)

const testServiceName = "node"

// newTestService builds the *services.Service that a chat handler resolves
// through the registry. Tests bypass the real services.Registry (which would
// require a running Tor process) and use chat.StaticLookup directly.
func newTestService(kp *identity.Keypair) *services.Service {
	return &services.Service{
		Name:     testServiceName,
		Address:  identity.DeriveAddress(kp.PublicKey()).String(),
		Identity: kp,
	}
}

// mockResolver implements PeerResolver for tests.
type mockResolver struct {
	peers map[string]*transport.PeerConnection
}

func (m *mockResolver) GetPeer(addr string) *transport.PeerConnection {
	return m.peers[addr]
}

// testPeer holds all state for one side of a chat test.
type testPeer struct {
	kp       *identity.Keypair
	addr     string
	host     *transport.Host
	bus      *node.EventBus
	resolver *mockResolver
	service  *Service
}

func newTestPeer(t *testing.T) *testPeer {
	t.Helper()
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	h, err := transport.NewHost(transport.HostConfig{Keypair: kp})
	if err != nil {
		t.Fatalf("creating host: %v", err)
	}
	t.Cleanup(func() { h.Close() })

	bus := node.NewEventBus()
	resolver := &mockResolver{peers: make(map[string]*transport.PeerConnection)}
	regSvc := newTestService(kp)
	svc := NewService(h, bus, resolver, nil, StaticLookup(regSvc), testServiceName)
	regSvc.Handlers = svc
	t.Cleanup(func() { svc.Close() })

	return &testPeer{
		kp:       kp,
		addr:     identity.DeriveAddress(kp.PublicKey()).String(),
		host:     h,
		bus:      bus,
		resolver: resolver,
		service:  svc,
	}
}

// connectPeers connects two test peers and populates their resolvers.
func connectPeers(t *testing.T, a, b *testPeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.host.Connect(ctx, b.host.AddrInfo()); err != nil {
		t.Fatalf("connecting hosts: %v", err)
	}

	a.resolver.peers[b.addr] = &transport.PeerConnection{
		Address: b.addr,
		PeerID:  b.host.ID(),
	}
	b.resolver.peers[a.addr] = &transport.PeerConnection{
		Address: a.addr,
		PeerID:  a.host.ID(),
	}
}

func TestSendAndReceive(t *testing.T) {
	alice := newTestPeer(t)
	bob := newTestPeer(t)
	connectPeers(t, alice, bob)

	// Subscribe to Bob's events before sending.
	ch := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msg, err := alice.service.SendMessage(ctx, bob.addr, "hello bob")
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}

	if msg.Text != "hello bob" {
		t.Fatalf("text: got %q, want %q", msg.Text, "hello bob")
	}
	if msg.From != alice.addr {
		t.Fatalf("from: got %q, want %q", msg.From, alice.addr)
	}
	if msg.To != bob.addr {
		t.Fatalf("to: got %q, want %q", msg.To, bob.addr)
	}
	if msg.Direction != Outgoing {
		t.Fatalf("direction: got %q, want %q", msg.Direction, Outgoing)
	}
	if msg.AckedAt == nil {
		t.Fatal("AckedAt should not be nil")
	}
	if msg.ID == "" {
		t.Fatal("message ID should not be empty")
	}

	// Bob should have received the message via event bus.
	select {
	case e := <-ch:
		received, ok := e.Payload.(*Message)
		if !ok {
			t.Fatalf("payload type: got %T, want *Message", e.Payload)
		}
		if received.Text != "hello bob" {
			t.Fatalf("received text: got %q, want %q", received.Text, "hello bob")
		}
		if received.From != alice.addr {
			t.Fatalf("received from: got %q, want %q", received.From, alice.addr)
		}
		if received.Direction != Incoming {
			t.Fatalf("received direction: got %q, want %q", received.Direction, Incoming)
		}
		if received.ID != msg.ID {
			t.Fatalf("message IDs should match: sent %q, received %q", msg.ID, received.ID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for chat event")
	}
}

func TestBidirectionalExchange(t *testing.T) {
	alice := newTestPeer(t)
	bob := newTestPeer(t)
	connectPeers(t, alice, bob)

	aliceCh := alice.bus.Subscribe(node.EventChatMessage)
	bobCh := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Alice → Bob
	msg1, err := alice.service.SendMessage(ctx, bob.addr, "hi bob")
	if err != nil {
		t.Fatalf("Alice→Bob SendMessage() error: %v", err)
	}

	select {
	case e := <-bobCh:
		r := e.Payload.(*Message)
		if r.ID != msg1.ID {
			t.Fatalf("Bob got wrong message ID")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Bob's event")
	}

	// Bob → Alice
	msg2, err := bob.service.SendMessage(ctx, alice.addr, "hi alice")
	if err != nil {
		t.Fatalf("Bob→Alice SendMessage() error: %v", err)
	}

	select {
	case e := <-aliceCh:
		r := e.Payload.(*Message)
		if r.ID != msg2.ID {
			t.Fatalf("Alice got wrong message ID")
		}
		if r.Text != "hi alice" {
			t.Fatalf("Alice received wrong text: %q", r.Text)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for Alice's event")
	}
}

func TestMultipleMessages(t *testing.T) {
	alice := newTestPeer(t)
	bob := newTestPeer(t)
	connectPeers(t, alice, bob)

	bobCh := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	messages := []string{"one", "two", "three", "four", "five"}
	ids := make([]string, len(messages))

	for i, text := range messages {
		msg, err := alice.service.SendMessage(ctx, bob.addr, text)
		if err != nil {
			t.Fatalf("SendMessage(%q) error: %v", text, err)
		}
		ids[i] = msg.ID
	}

	for i, text := range messages {
		select {
		case e := <-bobCh:
			r := e.Payload.(*Message)
			if r.Text != text {
				t.Fatalf("message %d: got %q, want %q", i, r.Text, text)
			}
			if r.ID != ids[i] {
				t.Fatalf("message %d: ID mismatch", i)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for message %d", i)
		}
	}
}

func TestPeerNotConnected(t *testing.T) {
	alice := newTestPeer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := alice.service.SendMessage(ctx, "EnotConnected", "hello")
	if err == nil {
		t.Fatal("expected error for unconnected peer")
	}
}

func TestUniqueMessageIDs(t *testing.T) {
	ids := make(map[string]bool)
	for range 100 {
		id := generateID()
		if ids[id] {
			t.Fatalf("duplicate ID: %s", id)
		}
		ids[id] = true
		if len(id) != 32 { // 16 bytes = 32 hex chars
			t.Fatalf("ID length: got %d, want 32", len(id))
		}
	}
}

func TestSendToInvalidPeer(t *testing.T) {
	alice := newTestPeer(t)
	bob := newTestPeer(t)

	// Register Bob in resolver but don't actually connect hosts.
	alice.resolver.peers[bob.addr] = &transport.PeerConnection{
		Address: bob.addr,
		PeerID:  peer.ID("invalid-peer-id"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := alice.service.SendMessage(ctx, bob.addr, "should fail")
	if err == nil {
		t.Fatal("expected error for invalid peer ID")
	}
}

// missingLookup always returns (nil, false).
type missingLookup struct{}

func (missingLookup) Get(string) (*services.Service, bool) { return nil, false }

// TestSendMessageWithoutRegisteredService verifies the registry-driven path:
// if the chat service's owning entry is not in the registry, SendMessage fails
// rather than falling back to a stale captured keypair.
func TestSendMessageWithoutRegisteredService(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}
	h, err := transport.NewHost(transport.HostConfig{Keypair: kp})
	if err != nil {
		t.Fatalf("creating host: %v", err)
	}
	t.Cleanup(func() { h.Close() })

	bus := node.NewEventBus()
	resolver := &mockResolver{peers: map[string]*transport.PeerConnection{
		"Esomeone": {Address: "Esomeone", PeerID: h.ID()},
	}}
	svc := NewService(h, bus, resolver, nil, missingLookup{}, "node")
	t.Cleanup(func() { svc.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = svc.SendMessage(ctx, "Esomeone", "hello")
	if err == nil {
		t.Fatal("expected SendMessage to fail when service is not registered")
	}
}

// TestEndToEndViaRegistry exercises the full chat path with a real
// services.Registry-shaped lookup: alice's chat handler is registered as the
// "node" service's Handlers; bob's is too. An inbound message on bob's
// libp2p stream resolves to bob's registered service through the lookup,
// type-asserts via Service.AsChat, and runs the standard ingest path.
func TestEndToEndViaRegistry(t *testing.T) {
	alice := newTestPeer(t)
	bob := newTestPeer(t)
	connectPeers(t, alice, bob)

	// Sanity-check: the chat service we built really is registered as the
	// node service's handlers and AsChat resolves to it.
	bobSvc, ok := bob.service.lookup.Get(testServiceName)
	if !ok {
		t.Fatal("expected lookup to find bob's node service")
	}
	if bobSvc.Handlers != bob.service {
		t.Fatal("expected bob's registered Handlers to be his chat.Service")
	}
	if h, ok := bobSvc.AsChat(); !ok || h != bob.service {
		t.Fatalf("bob.AsChat() = (%v, %v); want (chat.Service, true)", h, ok)
	}

	bobCh := bob.bus.Subscribe(node.EventChatMessage)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sent, err := alice.service.SendMessage(ctx, bob.addr, "via-registry")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case e := <-bobCh:
		got := e.Payload.(*Message)
		if got.Text != "via-registry" {
			t.Fatalf("text: got %q, want %q", got.Text, "via-registry")
		}
		if got.From != alice.addr {
			t.Fatalf("from: got %q, want %q", got.From, alice.addr)
		}
		if got.To != bob.addr {
			t.Fatalf("to: got %q, want %q", got.To, bob.addr)
		}
		if got.ID != sent.ID {
			t.Fatalf("ID mismatch: sent %q, recv %q", sent.ID, got.ID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for chat event")
	}
}

// TestOnChatMessageSatisfiesHandler verifies that chat.Service satisfies
// services.ChatHandler and that calling OnChatMessage directly publishes an
// EventChatMessage with the same payload shape consumers expect.
func TestOnChatMessageSatisfiesHandler(t *testing.T) {
	alice := newTestPeer(t)

	// Compile-time check that *Service satisfies services.ChatHandler.
	var _ services.ChatHandler = alice.service

	ch := alice.bus.Subscribe(node.EventChatMessage)

	if err := alice.service.OnChatMessage(context.Background(), "Eexternal", "from-handler"); err != nil {
		t.Fatalf("OnChatMessage: %v", err)
	}

	select {
	case e := <-ch:
		got := e.Payload.(*Message)
		if got.Text != "from-handler" {
			t.Fatalf("text: got %q, want %q", got.Text, "from-handler")
		}
		if got.From != "Eexternal" {
			t.Fatalf("from: got %q, want %q", got.From, "Eexternal")
		}
		if got.Direction != Incoming {
			t.Fatalf("direction: got %q, want %q", got.Direction, Incoming)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for synthesized chat event")
	}
}
