package signaling

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
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

// testPeer wires a single signaling.Server with a single service registration
// for one peer. Each peer gets its own keypair, contacts, listener, and
// fake-onion mapping in the shared dialer.
type testPeer struct {
	t       *testing.T
	name    string
	kp      *identity.Keypair
	addr    string
	onion   string
	server  *Server
	client  *Client
	ln      net.Listener
	dialer  *testDialer
	contact *contacts.Store

	mu        sync.Mutex
	onRequest func(*ConnectionRequest)
}

// peerOption mutates a testPeer's ServiceConfig before AddService is called.
type peerOption func(*ServiceConfig)

// withLocalAddrs configures the peer to advertise the given multiaddrs in
// the responder-side IP exchange.
func withLocalAddrs(addrs ...string) peerOption {
	return func(cfg *ServiceConfig) {
		cfg.LocalAddrs = func() []string { return addrs }
	}
}

func newTestPeer(t *testing.T, name string, dialer *testDialer, opts ...peerOption) *testPeer {
	t.Helper()

	kp, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	addr := identity.DeriveAddress(kp.PublicKey()).String()
	onion := addr[:8] + ".onion"

	cs := contacts.NewStore(t.TempDir())

	server := NewServer()
	client := NewClient(kp, addr, dialer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dialer.set(onion, ln.Addr().String())

	p := &testPeer{
		t:       t,
		name:    name,
		kp:      kp,
		addr:    addr,
		onion:   onion,
		server:  server,
		client:  client,
		ln:      ln,
		dialer:  dialer,
		contact: cs,
	}

	cfg := ServiceConfig{
		Name:     name,
		Keypair:  kp,
		Address:  addr,
		Contacts: cs,
		Listener: ln,
		OnRequest: func(cr *ConnectionRequest) {
			p.mu.Lock()
			cb := p.onRequest
			p.mu.Unlock()
			if cb != nil {
				cb(cr)
				return
			}
			cr.Decision <- ConnectionDecision{Accepted: false, Reason: "no handler"}
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := server.AddService(cfg); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		ln.Close()
		server.Stop()
	})

	return p
}

func (p *testPeer) setOnRequest(cb func(*ConnectionRequest)) {
	p.mu.Lock()
	p.onRequest = cb
	p.mu.Unlock()
}

func TestServerClient_KnownContact(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)

	bob.contact.Add(&contacts.Contact{Address: alice.addr, Alias: "Alice"})

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

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)

	bob.setOnRequest(func(cr *ConnectionRequest) {
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

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)

	bob.setOnRequest(func(cr *ConnectionRequest) {
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

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)
	// Bob's onRequest defaults to "no handler" rejection (set in newTestPeer).

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

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)

	bob.setOnRequest(func(cr *ConnectionRequest) {
		cr.Decision <- ConnectionDecision{Accepted: false, Reason: "nope"}
	})

	conn, err := net.Dial("tcp", dialer.get(bob.onion))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	reqData, _, _ := CreateConnRequest(alice.addr, bob.addr)
	reqEnv := wrapUnsigned(pb.MessageType_CONN_REQUEST, reqData)
	if err := writeEnvelope(conn, reqEnv); err != nil {
		t.Fatal(err)
	}

	respEnv, err := readEnvelope(conn)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := ValidateConnResponse(respEnv.Payload, nil, bob.addr)
	if err != nil && resp == nil {
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

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)

	bob.contact.Add(&contacts.Contact{Address: alice.addr})

	originalAddr := dialer.get(bob.onion)
	dialer.set(bob.onion, "127.0.0.1:1") // unreachable port

	go func() {
		time.Sleep(3 * time.Second)
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

// TestServiceIsolation_TwoServicesOnOneServer wires a single signaling.Server
// with two distinct services X and Y, each on its own listener with its own
// keypair and contacts. A peer dials X's onion: only X's handler runs, X's
// contacts are consulted, and Y's handler MUST NOT fire — the streams never
// cross over.
func TestServiceIsolation_TwoServicesOnOneServer(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	// Initiator (the "outside" peer that dials).
	alice := newTestPeer(t, "alice", dialer)

	// Responder runs both services on one Server.
	server := NewServer()
	t.Cleanup(func() {
		// Listeners are cleaned up before Stop via mkSvc's t.Cleanup.
		server.Stop()
	})

	type svc struct {
		name    string
		kp      *identity.Keypair
		addr    string
		onion   string
		ln      net.Listener
		cs      *contacts.Store
		callCnt atomic.Int64
	}

	mkSvc := func(name string) *svc {
		kp, err := identity.Generate()
		if err != nil {
			t.Fatal(err)
		}
		addr := identity.DeriveAddress(kp.PublicKey()).String()
		onion := name + addr[:8] + ".onion"
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		dialer.set(onion, ln.Addr().String())
		return &svc{name: name, kp: kp, addr: addr, onion: onion, ln: ln, cs: contacts.NewStore(t.TempDir())}
	}

	svcX := mkSvc("xservice")
	svcY := mkSvc("yservice")

	for _, s := range []*svc{svcX, svcY} {
		if err := server.AddService(ServiceConfig{
			Name:     s.name,
			Keypair:  s.kp,
			Address:  s.addr,
			Contacts: s.cs,
			Listener: s.ln,
			OnRequest: func(cr *ConnectionRequest) {
				s.callCnt.Add(1)
				cr.Decision <- ConnectionDecision{Accepted: true}
			},
		}); err != nil {
			t.Fatalf("AddService(%s): %v", s.name, err)
		}
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Alice dials only svcX. The handshake should resolve against svcX's keypair.
	result, err := alice.client.RequestConnection(ctx, svcX.onion, svcX.addr)
	if err != nil {
		t.Fatalf("RequestConnection X: %v", err)
	}
	if !identity.MatchesPublicKey(svcX.addr, result.PeerPubKey) {
		t.Fatal("X handshake produced wrong public key")
	}

	if got := svcX.callCnt.Load(); got != 1 {
		t.Errorf("svcX OnRequest fired %d times; want 1", got)
	}
	if got := svcY.callCnt.Load(); got != 0 {
		t.Errorf("svcY OnRequest fired %d times; want 0 (cross-service leak)", got)
	}

	// Now dial only svcY. svcX must remain at 1, svcY must increment to 1.
	result, err = alice.client.RequestConnection(ctx, svcY.onion, svcY.addr)
	if err != nil {
		t.Fatalf("RequestConnection Y: %v", err)
	}
	if !identity.MatchesPublicKey(svcY.addr, result.PeerPubKey) {
		t.Fatal("Y handshake produced wrong public key")
	}
	if got := svcX.callCnt.Load(); got != 1 {
		t.Errorf("svcX OnRequest fired %d times after Y dial; want 1", got)
	}
	if got := svcY.callCnt.Load(); got != 1 {
		t.Errorf("svcY OnRequest fired %d times; want 1", got)
	}

	// Wrong-target rejection: send a CONN_REQUEST to svcX targeting svcY's address.
	// svcX should reject (target mismatch) without invoking either handler.
	xCallsBefore := svcX.callCnt.Load()
	yCallsBefore := svcY.callCnt.Load()
	xCallTarget(t, dialer, svcX.onion, alice.addr, svcY.addr)
	if got := svcX.callCnt.Load(); got != xCallsBefore {
		t.Errorf("svcX OnRequest fired on wrong-target probe; want unchanged %d, got %d", xCallsBefore, got)
	}
	if got := svcY.callCnt.Load(); got != yCallsBefore {
		t.Errorf("svcY OnRequest fired on wrong-target probe; want unchanged %d, got %d", yCallsBefore, got)
	}
}

// TestServiceIsolation_PerServiceContacts confirms each service's contacts
// store is consulted independently — adding a contact to X must not affect Y.
func TestServiceIsolation_PerServiceContacts(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, "alice", dialer)

	server := NewServer()
	t.Cleanup(func() {
		server.Stop()
	})

	mkListener := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		return ln
	}

	xKp, _ := identity.Generate()
	xAddr := identity.DeriveAddress(xKp.PublicKey()).String()
	xOnion := "xservicepc" + xAddr[:8] + ".onion"
	xLn := mkListener()
	dialer.set(xOnion, xLn.Addr().String())
	xCS := contacts.NewStore(t.TempDir())

	yKp, _ := identity.Generate()
	yAddr := identity.DeriveAddress(yKp.PublicKey()).String()
	yOnion := "yservicepc" + yAddr[:8] + ".onion"
	yLn := mkListener()
	dialer.set(yOnion, yLn.Addr().String())
	yCS := contacts.NewStore(t.TempDir())

	// Only X knows Alice as a contact. Y rejects unknowns.
	xCS.Add(&contacts.Contact{Address: alice.addr, Alias: "Alice"})

	if err := server.AddService(ServiceConfig{
		Name:     "x",
		Keypair:  xKp,
		Address:  xAddr,
		Contacts: xCS,
		Listener: xLn,
		// No OnRequest — known contact path takes over.
	}); err != nil {
		t.Fatalf("AddService x: %v", err)
	}
	if err := server.AddService(ServiceConfig{
		Name:     "y",
		Keypair:  yKp,
		Address:  yAddr,
		Contacts: yCS,
		Listener: yLn,
		OnRequest: func(cr *ConnectionRequest) {
			cr.Decision <- ConnectionDecision{Accepted: false, Reason: "stranger"}
		},
	}); err != nil {
		t.Fatalf("AddService y: %v", err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := alice.client.RequestConnection(ctx, xOnion, xAddr); err != nil {
		t.Fatalf("Alice→X (known contact): %v", err)
	}
	if _, err := alice.client.RequestConnection(ctx, yOnion, yAddr); err == nil {
		t.Fatal("Alice→Y should have been rejected (stranger to Y)")
	} else if !errors.Is(err, ErrRejected) {
		t.Fatalf("Alice→Y err = %v, want ErrRejected", err)
	}
}

// TestServer_RemoveServiceStopsAccept ensures RemoveService halts the
// per-service accept loop without affecting other services.
func TestServer_RemoveServiceStopsAccept(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer)
	bob.contact.Add(&contacts.Contact{Address: alice.addr})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr); err != nil {
		t.Fatalf("baseline RequestConnection: %v", err)
	}

	if err := bob.server.RemoveService("bob"); err != nil {
		t.Fatalf("RemoveService: %v", err)
	}

	// Closing the listener detaches the local TCP from the dialer so
	// subsequent dials fail. RemoveService does not close the listener
	// itself (the Tor engine owns its lifecycle), so we do it here to
	// simulate the daemon's teardown path.
	bob.ln.Close()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer dialCancel()
	_, err := alice.client.RequestConnection(dialCtx, bob.onion, bob.addr)
	if err == nil {
		t.Fatal("expected RequestConnection to fail after RemoveService")
	}
}

// TestServer_EnvelopeHandlerRoutesNonSignaling confirms the EnvelopeHandler
// fires for messages that are not CONN_REQUEST. This is the seam the daemon
// uses to keep DHT routing on the same per-service onion listener.
func TestServer_EnvelopeHandlerRoutesNonSignaling(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}

	server := NewServer()

	var got atomic.Int64
	server.SetEnvelopeHandler(func(c net.Conn, env *pb.Envelope) {
		defer c.Close()
		if env.Type == pb.MessageType_DHT_FIND_NODE {
			got.Add(1)
		}
	})

	kp, _ := identity.Generate()
	addr := identity.DeriveAddress(kp.PublicKey()).String()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dialer.set("svc.onion", ln.Addr().String())

	if err := server.AddService(ServiceConfig{
		Name:     "svc",
		Keypair:  kp,
		Address:  addr,
		Contacts: contacts.NewStore(t.TempDir()),
		Listener: ln,
	}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ln.Close()
		server.Stop()
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	env := &pb.Envelope{
		Type:    pb.MessageType_DHT_FIND_NODE,
		Payload: []byte("ignored"),
	}
	if err := protocol.WriteMsg(conn, env); err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}
	conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got.Load() == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("EnvelopeHandler not invoked; got=%d", got.Load())
}

// xCallTarget dials the given fake onion and sends a CONN_REQUEST whose target
// address differs from the listening service's. The signaling layer must
// silently reject without invoking any per-service handler.
func xCallTarget(t *testing.T, dialer *testDialer, onion, fromAddr, targetAddr string) {
	t.Helper()
	conn, err := net.Dial("tcp", dialer.get(onion))
	if err != nil {
		t.Fatalf("dial %s: %v", onion, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	reqData, _, err := CreateConnRequest(fromAddr, targetAddr)
	if err != nil {
		t.Fatalf("CreateConnRequest: %v", err)
	}
	if err := protocol.WriteMsg(conn, wrapUnsigned(pb.MessageType_CONN_REQUEST, reqData)); err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}
	// Server should drop the connection without responding; reading should fail.
	if _, err := protocol.ReadMsg(conn); err == nil {
		t.Fatal("expected EOF after wrong-target probe; got a response")
	}
}

// writeEnvelope / readEnvelope use the protocol codec directly.
func writeEnvelope(conn net.Conn, env *pb.Envelope) error {
	return protocol.WriteMsg(conn, env)
}

func readEnvelope(conn net.Conn) (*pb.Envelope, error) {
	return protocol.ReadMsg(conn)
}

// TestServer_IPExchangeAfterHandshake is a regression test for a bug where
// the responder-side handler returned immediately after CONN_CONFIRM and
// closed the connection. The initiator's connector flow expects to do an
// encrypted IP exchange on the same Tor connection right after the 3-step
// handshake (so libp2p can NAT-traverse to the peer). Pre-fix the responder
// dropped that exchange and the initiator hung in
// "IP exchange: receiving IP info: reading length prefix: EOF". This test
// drives the full client-side flow against a real signaling.Server (with
// LocalAddrs configured) and asserts the responder's addrs come back.
func TestServer_IPExchangeAfterHandshake(t *testing.T) {
	bobAddrs := []string{
		"/ip4/192.168.1.42/udp/4001/quic-v1",
		"/ip4/127.0.0.1/udp/4001/quic-v1",
	}

	dialer := &testDialer{addrs: make(map[string]string)}
	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer, withLocalAddrs(bobAddrs...))
	bob.contact.Add(&contacts.Contact{Address: alice.addr, Alias: "Alice"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dialer.DialContext(ctx, bob.onion)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	// 3-step handshake (drives the client side by hand so the conn stays
	// open for the IP exchange that comes immediately after).
	reqData, reqNonce, err := CreateConnRequest(alice.addr, bob.addr)
	if err != nil {
		t.Fatalf("CreateConnRequest: %v", err)
	}
	if err := writeEnvelope(conn, wrapUnsigned(pb.MessageType_CONN_REQUEST, reqData)); err != nil {
		t.Fatalf("write CONN_REQUEST: %v", err)
	}

	respEnv, err := readEnvelope(conn)
	if err != nil {
		t.Fatalf("read CONN_RESPONSE: %v", err)
	}
	resp, err := ValidateConnResponse(respEnv.Payload, reqNonce, bob.addr)
	if err != nil {
		t.Fatalf("ValidateConnResponse: %v", err)
	}
	if !resp.Accepted {
		t.Fatalf("response rejected: %s", resp.Reason)
	}

	confirmData, err := CreateConnConfirm(alice.kp, resp.Nonce)
	if err != nil {
		t.Fatalf("CreateConnConfirm: %v", err)
	}
	if err := writeEnvelope(conn, wrapUnsigned(pb.MessageType_CONN_CONFIRM, confirmData)); err != nil {
		t.Fatalf("write CONN_CONFIRM: %v", err)
	}

	// IP exchange — the regression. Before the fix this read returned EOF
	// because the server closed the conn right after CONN_CONFIRM.
	aliceInfo := &IPInfo{Addrs: []string{"/ip4/10.0.0.1/udp/9001/quic-v1"}}
	gotInfo, err := ExchangeIPs(ctx, conn, aliceInfo, alice.kp, resp.PubKey)
	if err != nil {
		t.Fatalf("ExchangeIPs: %v", err)
	}

	if len(gotInfo.Addrs) != len(bobAddrs) {
		t.Fatalf("got %d addrs, want %d (%v)", len(gotInfo.Addrs), len(bobAddrs), gotInfo.Addrs)
	}
	for i, want := range bobAddrs {
		if gotInfo.Addrs[i] != want {
			t.Errorf("addrs[%d]: got %q, want %q", i, gotInfo.Addrs[i], want)
		}
	}
}

// TestServer_NoIPExchangeWithoutLocalAddrs confirms a service registered
// without LocalAddrs returns cleanly after CONN_CONFIRM (no IP exchange,
// no panic). This is the legacy path — the daemon always wires LocalAddrs
// in production, but tests and embedded uses without a libp2p host should
// still complete the handshake.
func TestServer_NoIPExchangeWithoutLocalAddrs(t *testing.T) {
	dialer := &testDialer{addrs: make(map[string]string)}
	alice := newTestPeer(t, "alice", dialer)
	bob := newTestPeer(t, "bob", dialer) // no LocalAddrs
	bob.contact.Add(&contacts.Contact{Address: alice.addr})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The signaling.Client only does the 3-step handshake; with no
	// LocalAddrs configured the server returns after CONN_CONFIRM, which
	// is exactly what the client expects.
	if _, err := alice.client.RequestConnection(ctx, bob.onion, bob.addr); err != nil {
		t.Fatalf("RequestConnection: %v", err)
	}
}
