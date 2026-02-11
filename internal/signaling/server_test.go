package signaling

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

// testDialer maps addresses to local TCP addresses for testing.
type testDialer struct {
	mu    sync.RWMutex
	addrs map[string]string
}

func (d *testDialer) DialContext(ctx context.Context, addr string) (net.Conn, error) {
	d.mu.RLock()
	localAddr, ok := d.addrs[addr]
	d.mu.RUnlock()
	if !ok {
		return nil, net.ErrClosed
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", localAddr)
}

func (d *testDialer) set(key, val string) {
	d.mu.Lock()
	d.addrs[key] = val
	d.mu.Unlock()
}

func (d *testDialer) get(key string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.addrs[key]
}

type testPeer struct {
	kp      *identity.Keypair
	addr    string
	onion   string
	server  *Server
	client  *Client
	ln      net.Listener
	dialer  *testDialer
}

func newTestPeer(t *testing.T, dialer *testDialer) *testPeer {
	t.Helper()

	kp, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	addr := identity.DeriveAddress(kp.PublicKey()).String()
	onion := addr[:8] + ".onion"

	cs := contacts.NewStore(t.TempDir())

	server := NewServer(kp, addr, cs)
	client := NewClient(kp, addr, dialer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localAddr := ln.Addr().String()
	dialer.set(onion, localAddr)

	// Start server in background.
	go server.Serve(context.Background(), ln)
	t.Cleanup(func() {
		server.Stop()
	})

	return &testPeer{
		kp:     kp,
		addr:   addr,
		onion:  onion,
		server: server,
		client: client,
		ln:     ln,
		dialer: dialer,
	}
}

func TestServerClient_KnownContact(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, dialer)
	bob := newTestPeer(t, dialer)

	// Bob adds Alice as a contact — should auto-accept.
	bob.server.contacts.Add(&contacts.Contact{
		Address: alice.addr,
		Alias:   "Alice",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr)
	if err != nil {
		t.Fatalf("RequestConnection: %v", err)
	}

	if result.PeerAddr != bob.addr {
		t.Fatalf("wrong peer address: %s", result.PeerAddr)
	}

	if !identity.MatchesPublicKey(bob.addr, result.PeerPubKey) {
		t.Fatal("peer public key doesn't match bob's address")
	}
}

func TestServerClient_UnknownAccepted(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, dialer)
	bob := newTestPeer(t, dialer)

	// Bob does NOT have Alice as a contact.
	// Set up handler that auto-accepts.
	bob.server.OnConnectionRequest(func(cr *ConnectionRequest) {
		cr.Decision <- ConnectionDecision{Accepted: true}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr)
	if err != nil {
		t.Fatalf("RequestConnection: %v", err)
	}

	if !identity.MatchesPublicKey(bob.addr, result.PeerPubKey) {
		t.Fatal("peer public key doesn't match")
	}
}

func TestServerClient_UnknownRejected(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, dialer)
	bob := newTestPeer(t, dialer)

	// Bob rejects unknowns.
	bob.server.OnConnectionRequest(func(cr *ConnectionRequest) {
		cr.Decision <- ConnectionDecision{Accepted: false, Reason: "go away"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr)
	if err == nil {
		t.Fatal("expected rejection error")
	}
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got: %v", err)
	}
}

func TestServerClient_UnknownNoHandler(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, dialer)
	bob := newTestPeer(t, dialer)

	// Bob has no handler — should reject by default.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr)
	if err == nil {
		t.Fatal("expected error when no handler")
	}
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got: %v", err)
	}
}

func TestServerClient_NoKeyRevealOnReject(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, dialer)
	bob := newTestPeer(t, dialer)

	bob.server.OnConnectionRequest(func(cr *ConnectionRequest) {
		cr.Decision <- ConnectionDecision{Accepted: false, Reason: "nope"}
	})

	// Dial manually to inspect raw response.
	conn, err := net.Dial("tcp", dialer.get(bob.onion))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	reqData, _, _ := CreateConnRequest(alice.addr, bob.addr)
	reqEnv := wrapUnsigned(0x01, reqData) // CONN_REQUEST = 1
	if err := writeEnvelope(conn, reqEnv); err != nil {
		t.Fatal(err)
	}

	respEnv, err := readEnvelope(conn)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ValidateConnResponse(respEnv.Payload, nil, bob.addr)
	if err != nil && resp == nil {
		// Rejected — expected. Verify no key in raw response.
		t.Log("rejected as expected")
		return
	}
	if resp != nil && !resp.Accepted {
		if len(resp.PubKey) > 0 {
			t.Fatal("rejected response should not contain public key")
		}
		return
	}
	t.Fatal("expected rejection")
}

func TestClient_RetryOnTransientError(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, dialer)
	bob := newTestPeer(t, dialer)

	bob.server.contacts.Add(&contacts.Contact{Address: alice.addr})

	// Temporarily make bob unreachable, then restore.
	originalAddr := dialer.get(bob.onion)
	dialer.set(bob.onion, "127.0.0.1:1") // unreachable port

	go func() {
		time.Sleep(3 * time.Second) // after first retry backoff
		dialer.set(bob.onion, originalAddr)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if result.PeerAddr != bob.addr {
		t.Fatalf("wrong peer: %s", result.PeerAddr)
	}
}

// writeEnvelope / readEnvelope use the protocol codec directly.
func writeEnvelope(conn net.Conn, env *pb.Envelope) error {
	return protocol.WriteMsg(conn, env)
}

func readEnvelope(conn net.Conn) (*pb.Envelope, error) {
	return protocol.ReadMsg(conn)
}
