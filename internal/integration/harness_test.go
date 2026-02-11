package integration

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/chat"
	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/filetransfer"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/transport"
)

// testDialer maps fake "onion" addresses to local TCP addresses for testing.
type testDialer struct {
	mu    sync.RWMutex
	addrs map[string]string
}

func newTestDialer() *testDialer {
	return &testDialer{addrs: make(map[string]string)}
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

// mockFinder implements transport.PeerFinder, returning pre-configured onion addresses.
type mockFinder struct {
	mu      sync.RWMutex
	entries map[string]string // ensemble address → fake onion address
}

func newMockFinder() *mockFinder {
	return &mockFinder{entries: make(map[string]string)}
}

func (f *mockFinder) set(addr, onion string) {
	f.mu.Lock()
	f.entries[addr] = onion
	f.mu.Unlock()
}

func (f *mockFinder) FindPeer(_ context.Context, addr string) (transport.PeerLookupResult, error) {
	f.mu.RLock()
	onion, ok := f.entries[addr]
	f.mu.RUnlock()
	if !ok {
		return transport.PeerLookupResult{}, net.ErrClosed
	}
	return transport.PeerLookupResult{
		Address:   addr,
		OnionAddr: onion,
	}, nil
}

// connectorResolver adapts a transport.Connector to the PeerResolver interface
// used by chat.Service and filetransfer.Sender/Receiver.
type connectorResolver struct {
	connector *transport.Connector
}

func (r *connectorResolver) GetPeer(addr string) *transport.PeerConnection {
	return r.connector.GetPeer(addr)
}

// testPeer represents a fully wired peer stack for integration testing.
type testPeer struct {
	name string

	kp      *identity.Keypair
	addr    string
	onion   string
	dataDir string

	contacts *contacts.Store
	bus      *node.EventBus
	host     *transport.Host
	presence *transport.Presence

	sigLn     net.Listener
	connector *transport.Connector
	resolver  *connectorResolver

	chatSvc    *chat.Service
	chatHist   *chat.History
	ftSender   *filetransfer.Sender
	ftReceiver *filetransfer.Receiver
	ftSaveDir  string

	dialer *testDialer
	finder *mockFinder

	// onReqCb overrides the accept/reject behavior for unknown peers.
	// If nil, unknown peers are rejected by default.
	onReqMu sync.Mutex
	onReqCb func(fromAddr string) signaling.ConnectionDecision
}

// newTestPeer builds a fully wired peer stack for one side of the test.
// The responder side runs a custom signaling handler that performs the
// 3-step handshake + encrypted IP exchange (the signaling.Server only does
// the handshake; the Connector's connectFlow expects IP exchange on the
// same connection).
func newTestPeer(t *testing.T, name string, dialer *testDialer, finder *mockFinder) *testPeer {
	t.Helper()

	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("%s: generating keypair: %v", name, err)
	}
	addr := identity.DeriveAddress(kp.PublicKey()).String()
	onion := addr[:8] + ".onion"
	dataDir := t.TempDir()

	// Contacts store.
	cs := contacts.NewStore(dataDir)

	// Event bus.
	bus := node.NewEventBus()

	// libp2p host (random port, no NAT).
	host, err := transport.NewHost(transport.HostConfig{
		Keypair:          kp,
		ListenPort:       0,
		DisableNATMap:    true,
		DisableHolePunch: true,
	})
	if err != nil {
		t.Fatalf("%s: creating host: %v", name, err)
	}
	t.Cleanup(func() { host.Close() })

	// Presence service.
	pres := transport.NewPresence(host, kp)
	t.Cleanup(func() { pres.Close() })

	// TCP listener for signaling.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("%s: listening for signaling: %v", name, err)
	}
	dialer.set(onion, ln.Addr().String())

	// Register this peer in the mock finder.
	finder.set(addr, onion)

	// State events → event bus.
	onState := func(se transport.StateEvent) {
		bus.Publish(node.Event{
			Type:    node.EventConnectionState,
			Payload: se,
		})
	}

	// Connector.
	conn := transport.NewConnector(transport.ConnectorConfig{
		Keypair: kp,
		Host:    host,
		Finder:  finder,
		Dialer:  dialer,
		NATCfg: transport.NATConfig{
			DirectTimeout:    5 * time.Second,
			HolePunchTimeout: 500 * time.Millisecond,
			RelayTimeout:     500 * time.Millisecond,
		},
		OnState:  onState,
		MaxPeers: 50,
		MaxXfers: 5,
	})

	resolver := &connectorResolver{connector: conn}

	// Chat service with history.
	hist := chat.NewHistory()
	chatSvc := chat.NewService(kp, host, bus, resolver, hist)
	t.Cleanup(func() { chatSvc.Close() })

	// File transfer sender and receiver.
	ftSaveDir := t.TempDir()
	sender := filetransfer.NewSender(kp, host, resolver)
	receiver := filetransfer.NewReceiver(kp, host, bus, ftSaveDir)
	t.Cleanup(func() { receiver.Close() })

	p := &testPeer{
		name:       name,
		kp:         kp,
		addr:       addr,
		onion:      onion,
		dataDir:    dataDir,
		contacts:   cs,
		bus:        bus,
		host:       host,
		presence:   pres,
		sigLn:      ln,
		connector:  conn,
		resolver:   resolver,
		chatSvc:    chatSvc,
		chatHist:   hist,
		ftSender:   sender,
		ftReceiver: receiver,
		ftSaveDir:  ftSaveDir,
		dialer:     dialer,
		finder:     finder,
	}

	// Start the custom signaling responder that handles handshake + IP exchange.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { ln.Close() })
	go p.serveSignaling(ctx)

	return p
}

// serveSignaling runs a custom signaling responder loop.
// For each inbound TCP connection, it performs:
//  1. 3-step handshake (ConnRequest → ConnResponse → ConnConfirm)
//  2. Encrypted IP exchange
//
// This matches what the Connector's connectFlow expects on the initiator side.
func (p *testPeer) serveSignaling(ctx context.Context) {
	nc := signaling.NewNonceCache()
	for {
		conn, err := p.sigLn.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				return
			}
		}
		go p.handleSignalingConn(ctx, conn, nc)
	}
}

func (p *testPeer) handleSignalingConn(ctx context.Context, conn net.Conn, nc *signaling.NonceCache) {
	defer conn.Close()

	// Step 1: Read ConnRequest.
	env, err := protocol.ReadMsg(conn)
	if err != nil {
		return
	}
	if env.Type != pb.MessageType_CONN_REQUEST {
		return
	}
	req, err := signaling.ValidateConnRequest(env.Payload, nc)
	if err != nil {
		return
	}
	if req.TargetAddress != p.addr {
		return
	}

	// Contact gating.
	accepted := false
	reason := ""
	if c := p.contacts.Get(req.FromAddress); c != nil {
		accepted = true
	} else {
		p.onReqMu.Lock()
		cb := p.onReqCb
		p.onReqMu.Unlock()
		if cb != nil {
			d := cb(req.FromAddress)
			accepted = d.Accepted
			reason = d.Reason
		} else {
			reason = "no handler"
		}
	}

	// Step 2: Send ConnResponse.
	if !accepted {
		respData, _, _ := signaling.CreateConnResponse(false, nil, nil, reason)
		protocol.WriteMsg(conn, &pb.Envelope{Type: pb.MessageType_CONN_RESPONSE, Payload: respData})
		return
	}

	respData, respNonce, err := signaling.CreateConnResponse(true, p.kp, req.Nonce, "")
	if err != nil {
		return
	}
	if err := protocol.WriteMsg(conn, &pb.Envelope{Type: pb.MessageType_CONN_RESPONSE, Payload: respData}); err != nil {
		return
	}

	// Step 3: Read ConnConfirm.
	confirmEnv, err := protocol.ReadMsg(conn)
	if err != nil {
		return
	}
	if confirmEnv.Type != pb.MessageType_CONN_CONFIRM {
		return
	}
	confirmResult, err := signaling.ValidateConnConfirm(confirmEnv.Payload, respNonce, req.FromAddress)
	if err != nil {
		return
	}

	// IP Exchange — send our host addrs, receive initiator's.
	ourInfo := &signaling.IPInfo{Addrs: p.host.Addrs()}
	ipCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var peerPubKey []byte
	if confirmResult != nil {
		peerPubKey = confirmResult.PubKey
	}
	if len(peerPubKey) == 0 {
		// Fallback: derive from the fromAddress's public key if the confirm included it.
		return
	}
	signaling.ExchangeIPs(ipCtx, conn, ourInfo, p.kp, peerPubKey)
}

// addContact adds the other peer as a known contact so signaling auto-accepts.
func (p *testPeer) addContact(other *testPeer) {
	p.contacts.Add(&contacts.Contact{
		Address:   other.addr,
		Alias:     other.name,
		PublicKey: other.kp.PublicKey(),
		OnionAddr: other.onion,
	})
}

// setOnConnectionRequest sets the callback for unknown connection requests.
func (p *testPeer) setOnConnectionRequest(cb func(fromAddr string) signaling.ConnectionDecision) {
	p.onReqMu.Lock()
	p.onReqCb = cb
	p.onReqMu.Unlock()
}

// connectViaConnector runs the full Connector flow to establish a connection.
func connectViaConnector(t *testing.T, initiator, responder *testPeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pc, err := initiator.connector.Connect(ctx, responder.addr)
	if err != nil {
		t.Fatalf("Connect(%s→%s): %v", initiator.name, responder.name, err)
	}
	if pc.State != transport.StateConnected && pc.State != transport.StateConnectedTor {
		t.Fatalf("expected connected state, got %s", pc.State)
	}
}
