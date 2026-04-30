package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apipb "github.com/boxsie/ensemble/api/pb"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/services"
	"github.com/boxsie/ensemble/internal/tor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// fakeOnionEngine is an in-memory stand-in for *tor.Engine that satisfies
// services.OnionEngine. Synthetic .onion addresses derive from the service
// name so tests can assert exactly which onion belongs to which service.
type fakeOnionEngine struct {
	mu    sync.Mutex
	addrs map[string]string
}

func newFakeOnionEngine() *fakeOnionEngine {
	return &fakeOnionEngine{addrs: make(map[string]string)}
}

func (f *fakeOnionEngine) AddOnion(_ context.Context, name string, _ tor.Keypair) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.addrs[name]; ok {
		return "", tor.ErrOnionExists
	}
	addr := fmt.Sprintf("%sfakeaddr.onion", name)
	f.addrs[name] = addr
	return addr, nil
}

func (f *fakeOnionEngine) RemoveOnion(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.addrs[name]; !ok {
		return tor.ErrOnionNotFound
	}
	delete(f.addrs, name)
	return nil
}

func (f *fakeOnionEngine) running(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.addrs[name]
	return ok
}

// captureDispatcher is a ChatDispatcher that records every Send call.
type captureDispatcher struct {
	mu    sync.Mutex
	sends []chatSendRecord
	err   error
}

type chatSendRecord struct {
	Service string
	ToAddr  string
	Text    string
}

func (c *captureDispatcher) Send(_ context.Context, fromService, toAddr, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends = append(c.sends, chatSendRecord{Service: fromService, ToAddr: toAddr, Text: text})
	if c.err != nil {
		return "", c.err
	}
	return "msg-" + toAddr, nil
}

func (c *captureDispatcher) records() []chatSendRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]chatSendRecord, len(c.sends))
	copy(out, c.sends)
	return out
}

// testHarness wires an in-memory gRPC server with a real registry backed by
// a fake onion engine. Tests get back a client to drive the bidi stream and
// the underlying registry / dispatcher / engine for assertions.
type testHarness struct {
	t          *testing.T
	registry   *services.Registry
	engine     *fakeOnionEngine
	dispatcher *captureDispatcher
	srv        *Server
	grpc       *grpc.Server
	conn       *grpc.ClientConn
	client     apipb.EnsembleServiceClient
	listener   *bufconn.Listener
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	dir := t.TempDir()
	ks, err := identity.NewKeystore(dir, "test-passphrase")
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	engine := newFakeOnionEngine()
	registry := services.New(ks, engine)
	dispatcher := &captureDispatcher{}

	srv := &Server{}
	srv.SetRegistry(registry)
	srv.SetChatDispatcher(dispatcher)

	gs := grpc.NewServer()
	apipb.RegisterEnsembleServiceServer(gs, srv)

	lis := bufconn.Listen(1 << 16)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(context.Background())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	client := apipb.NewEnsembleServiceClient(conn)

	h := &testHarness{
		t:          t,
		registry:   registry,
		engine:     engine,
		dispatcher: dispatcher,
		srv:        srv,
		grpc:       gs,
		conn:       conn,
		client:     client,
		listener:   lis,
	}
	t.Cleanup(h.Close)
	return h
}

func (h *testHarness) Close() {
	_ = h.conn.Close()
	h.grpc.Stop()
	_ = h.listener.Close()
}

// register opens a RegisterService stream, sends the manifest, and waits for
// the ServiceRegistered reply. Returns the live stream so the test can drive
// further interactions.
func (h *testHarness) register(ctx context.Context, manifest *apipb.ServiceManifest) (apipb.EnsembleService_RegisterServiceClient, *apipb.ServiceRegistered) {
	h.t.Helper()
	stream, err := h.client.RegisterService(ctx)
	if err != nil {
		h.t.Fatalf("RegisterService: %v", err)
	}
	if err := stream.Send(&apipb.ServiceClientMessage{
		Msg: &apipb.ServiceClientMessage_Manifest{Manifest: manifest},
	}); err != nil {
		h.t.Fatalf("Send manifest: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		h.t.Fatalf("Recv reply: %v", err)
	}
	reg, ok := resp.GetMsg().(*apipb.ServiceServerMessage_Registered)
	if !ok {
		h.t.Fatalf("expected ServiceRegistered, got %T (%+v)", resp.GetMsg(), resp)
	}
	return stream, reg.Registered
}

// recvNext reads the next ServiceServerMessage with a deadline.
func recvNext(t *testing.T, stream apipb.EnsembleService_RegisterServiceClient) *apipb.ServiceServerMessage {
	t.Helper()
	ch := make(chan *apipb.ServiceServerMessage, 1)
	errCh := make(chan error, 1)
	go func() {
		m, err := stream.Recv()
		if err != nil {
			errCh <- err
			return
		}
		ch <- m
	}()
	select {
	case m := <-ch:
		return m
	case err := <-errCh:
		t.Fatalf("recv: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("recv: timeout")
	}
	return nil
}

// TestRegisterService_HappyPath exercises the full lifecycle: manifest →
// registered → SendMessage action → dispatcher captures the call.
func TestRegisterService_HappyPath(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, reg := h.register(ctx, &apipb.ServiceManifest{
		Name:        "testbot",
		Description: "test bot",
		Acl:         apipb.ACL_ACL_CONTACTS,
	})
	defer stream.CloseSend()

	if reg.Address == "" || reg.Address[0] != 'E' {
		t.Errorf("Address = %q, want non-empty E-prefixed", reg.Address)
	}
	if reg.Onion != "testbotfakeaddr.onion" {
		t.Errorf("Onion = %q, want testbotfakeaddr.onion", reg.Onion)
	}
	svc, ok := h.registry.Get("testbot")
	if !ok {
		t.Fatal("registry: service not registered")
	}
	if svc.Manifest.ACL != services.ACLContacts {
		t.Errorf("Manifest.ACL = %v, want ACLContacts", svc.Manifest.ACL)
	}

	if err := stream.Send(&apipb.ServiceClientMessage{
		Msg: &apipb.ServiceClientMessage_SendMessage{
			SendMessage: &apipb.ServiceSendMessage{
				ToAddr: "Eabcdef",
				Text:   "hello",
			},
		},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The dispatcher captures asynchronously; poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs := h.dispatcher.records()
		if len(recs) == 1 {
			if recs[0].Service != "testbot" {
				t.Errorf("dispatcher.Service = %q, want testbot", recs[0].Service)
			}
			if recs[0].ToAddr != "Eabcdef" {
				t.Errorf("dispatcher.ToAddr = %q", recs[0].ToAddr)
			}
			if recs[0].Text != "hello" {
				t.Errorf("dispatcher.Text = %q", recs[0].Text)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dispatcher never captured the SendMessage; records=%v", h.dispatcher.records())
}

// TestRegisterService_FirstMessageMustBeManifest sends a SendMessage as the
// very first message and expects a ServiceError followed by stream close.
func TestRegisterService_FirstMessageMustBeManifest(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.RegisterService(ctx)
	if err != nil {
		t.Fatalf("RegisterService: %v", err)
	}
	if err := stream.Send(&apipb.ServiceClientMessage{
		Msg: &apipb.ServiceClientMessage_SendMessage{
			SendMessage: &apipb.ServiceSendMessage{ToAddr: "E", Text: "x"},
		},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_ = stream.CloseSend()

	resp := recvNext(t, stream)
	errMsg, ok := resp.GetMsg().(*apipb.ServiceServerMessage_Error)
	if !ok {
		t.Fatalf("expected ServiceError, got %T", resp.GetMsg())
	}
	if errMsg.Error.Message == "" {
		t.Error("ServiceError.Message is empty")
	}

	// Stream should close.
	if _, err := stream.Recv(); err == nil {
		t.Error("expected stream close after first-message error")
	}
}

// TestRegisterService_InvalidManifest covers missing name, bad ACL value, and
// invalid allowlist entries.
func TestRegisterService_InvalidManifest(t *testing.T) {
	cases := []struct {
		name     string
		manifest *apipb.ServiceManifest
	}{
		{"empty name", &apipb.ServiceManifest{Name: "", Acl: apipb.ACL_ACL_PUBLIC}},
		{"bad acl", &apipb.ServiceManifest{Name: "x", Acl: apipb.ACL(99)}},
		{"bad allowlist", &apipb.ServiceManifest{
			Name:      "y",
			Acl:       apipb.ACL_ACL_ALLOWLIST,
			Allowlist: []string{"not-an-address"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := h.client.RegisterService(ctx)
			if err != nil {
				t.Fatalf("RegisterService: %v", err)
			}
			if err := stream.Send(&apipb.ServiceClientMessage{
				Msg: &apipb.ServiceClientMessage_Manifest{Manifest: tc.manifest},
			}); err != nil {
				t.Fatalf("Send manifest: %v", err)
			}
			_ = stream.CloseSend()

			resp := recvNext(t, stream)
			if _, ok := resp.GetMsg().(*apipb.ServiceServerMessage_Error); !ok {
				t.Fatalf("expected ServiceError, got %T", resp.GetMsg())
			}
		})
	}
}

// TestRegisterService_DisconnectUnregisters confirms that closing the gRPC
// stream tears down the onion and clears the registry, while leaving the
// keystore entry in place so reconnect under the same name is stable.
func TestRegisterService_DisconnectUnregisters(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, _ := h.register(ctx, &apipb.ServiceManifest{
		Name: "testbot",
		Acl:  apipb.ACL_ACL_CONTACTS,
	})

	if !h.engine.running("testbot") {
		t.Fatal("engine: testbot not running after register")
	}

	// Cancel the context — this terminates the stream from the client side.
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.registry.Get("testbot"); !ok && !h.engine.running("testbot") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := h.registry.Get("testbot"); ok {
		t.Error("registry: testbot still registered after disconnect")
	}
	if h.engine.running("testbot") {
		t.Error("engine: testbot still running after disconnect")
	}
	_ = stream
}

// TestRegisterService_ReconnectSameAddress confirms the keystore-retained
// keypair gives the same E-address after a fresh registration of the same
// service name.
func TestRegisterService_ReconnectSameAddress(t *testing.T) {
	h := newTestHarness(t)

	first := func() string {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, reg := h.register(ctx, &apipb.ServiceManifest{
			Name: "testbot",
			Acl:  apipb.ACL_ACL_CONTACTS,
		})
		defer stream.CloseSend()
		// Cancel triggers cleanup.
		cancel()
		// Wait for unregister to land.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := h.registry.Get("testbot"); !ok {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		return reg.Address
	}

	addr1 := first()

	// Re-register from scratch.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	stream2, reg2 := h.register(ctx2, &apipb.ServiceManifest{
		Name: "testbot",
		Acl:  apipb.ACL_ACL_CONTACTS,
	})
	defer stream2.CloseSend()

	if reg2.Address != addr1 {
		t.Errorf("addr2 = %q, want %q (reconnect under same name should reuse keypair)", reg2.Address, addr1)
	}
}

// TestRegisterService_Backpressure floods the per-stream queue without
// reading any events for long enough that the in-process channel must drop
// some. Asserts that drops are observable, the stream stays alive after the
// flood drains, and a post-flood event still arrives.
func TestRegisterService_Backpressure(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, _ := h.register(ctx, &apipb.ServiceManifest{
		Name: "testbot",
		Acl:  apipb.ACL_ACL_PUBLIC,
	})
	defer stream.CloseSend()

	svc, ok := h.registry.Get("testbot")
	if !ok {
		t.Fatal("registry: testbot not registered")
	}
	chatHandler, ok := svc.AsChat()
	if !ok {
		t.Fatal("AsChat: false")
	}

	// Flood far more than eventQueueSize events while the client doesn't
	// read. Once the in-process channel fills and gRPC transport buffers
	// fill, the forwarder stalls and emit() drops oldest events.
	const flood = eventQueueSize * 8
	var emitted atomic.Int64
	for i := 0; i < flood; i++ {
		if err := chatHandler.OnChatMessage(ctx, "Esender", fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatalf("OnChatMessage(%d): %v", i, err)
		}
		emitted.Add(1)
	}

	// Emit the sentinel right away so it's queued behind the flood. Drain
	// straight through until we see it. Drops happen during emit; once the
	// channel drains, the sentinel is delivered last (modulo gRPC ordering).
	if err := chatHandler.OnChatMessage(ctx, "Esender", "sentinel"); err != nil {
		t.Fatalf("OnChatMessage(sentinel): %v", err)
	}

	delivered := 0
	for ctx.Err() == nil {
		m, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv during drain: %v", err)
		}
		delivered++
		ev, ok := m.GetMsg().(*apipb.ServiceServerMessage_Event)
		if !ok {
			continue
		}
		payload := &apipb.ServiceChatMessageEvent{}
		if err := proto.Unmarshal(ev.Event.Payload, payload); err != nil {
			continue
		}
		if payload.Text == "sentinel" {
			// Drops are the deterministic backpressure signal.
			if delivered >= int(emitted.Load())+1 {
				t.Errorf("delivered %d, emitted %d (incl. sentinel) — expected at least one drop", delivered, emitted.Load()+1)
			}
			if delivered == 0 {
				t.Error("delivered 0 events; expected at least one")
			}
			return
		}
	}
	t.Fatalf("ctx expired before sentinel arrived; delivered=%d", delivered)
}

// TestRegisterService_AcceptConnection drives an inbound connection request
// through the registry's ConnectionHandler interface and verifies the gRPC
// client can accept it via ServiceAcceptConnection.
func TestRegisterService_AcceptConnection(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, _ := h.register(ctx, &apipb.ServiceManifest{
		Name: "testbot",
		Acl:  apipb.ACL_ACL_CONTACTS,
	})
	defer stream.CloseSend()

	svc, _ := h.registry.Get("testbot")
	connHandler, ok := svc.AsConnection()
	if !ok {
		t.Fatal("AsConnection: false")
	}

	// Simulate the signaling layer asking the handler about an unknown peer.
	type result struct {
		accepted bool
		err      error
	}
	resCh := make(chan result, 1)
	go func() {
		acc, err := connHandler.OnConnectionRequest(ctx, services.ConnectionRequest{FromAddr: "Estranger"})
		resCh <- result{acc, err}
	}()

	// Read the connection_request event and capture its request_id.
	resp := recvNext(t, stream)
	ev, ok := resp.GetMsg().(*apipb.ServiceServerMessage_Event)
	if !ok {
		t.Fatalf("expected ServiceEvent, got %T", resp.GetMsg())
	}
	if ev.Event.Type != "connection_request" {
		t.Fatalf("event type = %q, want connection_request", ev.Event.Type)
	}
	payload := &apipb.ServiceConnectionRequestEvent{}
	if err := proto.Unmarshal(ev.Event.Payload, payload); err != nil {
		t.Fatalf("unmarshal connection_request: %v", err)
	}
	if payload.FromAddr != "Estranger" {
		t.Errorf("FromAddr = %q, want Estranger", payload.FromAddr)
	}
	if payload.RequestId == "" {
		t.Fatal("RequestId is empty")
	}

	// Accept the request via the gRPC stream.
	if err := stream.Send(&apipb.ServiceClientMessage{
		Msg: &apipb.ServiceClientMessage_Accept{
			Accept: &apipb.ServiceAcceptConnection{RequestId: payload.RequestId},
		},
	}); err != nil {
		t.Fatalf("Send accept: %v", err)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("OnConnectionRequest err: %v", r.err)
		}
		if !r.accepted {
			t.Error("OnConnectionRequest returned accepted=false; expected true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnConnectionRequest never returned after Accept")
	}
}

// TestRegisterService_RejectConnection mirrors the accept test with reject.
func TestRegisterService_RejectConnection(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, _ := h.register(ctx, &apipb.ServiceManifest{
		Name: "testbot",
		Acl:  apipb.ACL_ACL_CONTACTS,
	})
	defer stream.CloseSend()

	svc, _ := h.registry.Get("testbot")
	connHandler, _ := svc.AsConnection()

	type result struct {
		accepted bool
		err      error
	}
	resCh := make(chan result, 1)
	go func() {
		acc, err := connHandler.OnConnectionRequest(ctx, services.ConnectionRequest{FromAddr: "Estranger"})
		resCh <- result{acc, err}
	}()

	resp := recvNext(t, stream)
	ev := resp.GetMsg().(*apipb.ServiceServerMessage_Event)
	payload := &apipb.ServiceConnectionRequestEvent{}
	_ = proto.Unmarshal(ev.Event.Payload, payload)

	if err := stream.Send(&apipb.ServiceClientMessage{
		Msg: &apipb.ServiceClientMessage_Reject{
			Reject: &apipb.ServiceRejectConnection{
				RequestId: payload.RequestId,
				Reason:    "go away",
			},
		},
	}); err != nil {
		t.Fatalf("Send reject: %v", err)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("OnConnectionRequest err: %v", r.err)
		}
		if r.accepted {
			t.Error("OnConnectionRequest returned accepted=true; expected false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnConnectionRequest never returned after Reject")
	}
}

// TestRegisterService_AlreadyRegistered confirms a second client trying to
// register the same name while the first is still live gets ErrAlreadyRegistered
// surfaced as a ServiceError.
func TestRegisterService_AlreadyRegistered(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream1, _ := h.register(ctx, &apipb.ServiceManifest{Name: "testbot", Acl: apipb.ACL_ACL_PUBLIC})
	defer stream1.CloseSend()

	stream2, err := h.client.RegisterService(ctx)
	if err != nil {
		t.Fatalf("RegisterService 2: %v", err)
	}
	if err := stream2.Send(&apipb.ServiceClientMessage{
		Msg: &apipb.ServiceClientMessage_Manifest{
			Manifest: &apipb.ServiceManifest{Name: "testbot", Acl: apipb.ACL_ACL_PUBLIC},
		},
	}); err != nil {
		t.Fatalf("Send manifest 2: %v", err)
	}
	_ = stream2.CloseSend()

	resp := recvNext(t, stream2)
	errResp, ok := resp.GetMsg().(*apipb.ServiceServerMessage_Error)
	if !ok {
		t.Fatalf("expected ServiceError, got %T", resp.GetMsg())
	}
	if errResp.Error.Message == "" {
		t.Error("ServiceError.Message is empty")
	}
	// Recv should now return EOF (stream closed by server).
	if _, err := stream2.Recv(); err == nil {
		t.Error("expected stream close after AlreadyRegistered")
	} else if !errors.Is(err, io.EOF) {
		// gRPC-go often returns a status error rather than io.EOF on stream
		// close; that's fine. Accept anything non-nil.
		_ = err
	}
}

// TestRegisterService_RegistryNotConfigured verifies the handler errors
// cleanly when the daemon hasn't wired a registry yet.
func TestRegisterService_RegistryNotConfigured(t *testing.T) {
	srv := &Server{}
	gs := grpc.NewServer()
	apipb.RegisterEnsembleServiceServer(gs, srv)

	lis := bufconn.Listen(1 << 16)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(context.Background())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()
	client := apipb.NewEnsembleServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.RegisterService(ctx)
	if err != nil {
		t.Fatalf("RegisterService: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Error("expected error when registry is not configured")
	}
}

// Tests below cover proto-level oneof dispatch in isolation (no gRPC).

func TestValidateManifest_Cases(t *testing.T) {
	cases := []struct {
		name    string
		m       *apipb.ServiceManifest
		wantErr bool
	}{
		{"nil", nil, true},
		{"empty name", &apipb.ServiceManifest{Name: ""}, true},
		{"valid public", &apipb.ServiceManifest{Name: "x", Acl: apipb.ACL_ACL_PUBLIC}, false},
		{"valid contacts", &apipb.ServiceManifest{Name: "x", Acl: apipb.ACL_ACL_CONTACTS}, false},
		{"unknown acl", &apipb.ServiceManifest{Name: "x", Acl: apipb.ACL(42)}, true},
		{"allowlist with empty list", &apipb.ServiceManifest{Name: "x", Acl: apipb.ACL_ACL_ALLOWLIST}, false},
		{"allowlist with bad addr", &apipb.ServiceManifest{
			Name:      "x",
			Acl:       apipb.ACL_ACL_ALLOWLIST,
			Allowlist: []string{"bogus"},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManifest(tc.m)
			if (err != nil) != tc.wantErr {
				t.Errorf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestACLFromProto(t *testing.T) {
	cases := []struct {
		in   apipb.ACL
		want services.ACL
	}{
		{apipb.ACL_ACL_PUBLIC, services.ACLPublic},
		{apipb.ACL_ACL_CONTACTS, services.ACLContacts},
		{apipb.ACL_ACL_ALLOWLIST, services.ACLAllowlist},
		{apipb.ACL(99), services.ACLPublic},
	}
	for _, tc := range cases {
		got := aclFromProto(tc.in)
		if got != tc.want {
			t.Errorf("aclFromProto(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
