package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/boxsie/ensemble/internal/api"
	"github.com/boxsie/ensemble/internal/chat"
	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/discovery"
	"github.com/boxsie/ensemble/internal/filetransfer"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/services"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/tor"
	"github.com/boxsie/ensemble/internal/transport"
	"google.golang.org/grpc"
)

// nodeHandlers is the composite Handlers value attached to the registered
// "node" service. Embedding gives the registry's role-based type assertions
// (svc.AsChat / svc.AsFile) a single value that satisfies both
// services.ChatHandler (via *chat.Service) and services.FileHandler (via
// *filetransfer.Receiver). Keeping the struct in the daemon package avoids
// any cross-import between chat and filetransfer.
type nodeHandlers struct {
	*chat.Service
	*filetransfer.Receiver
}

// Daemon manages the node lifecycle and gRPC server.
type Daemon struct {
	cfg             Config
	node            *node.Node
	grpcServer      *grpc.Server
	apiServer       *api.Server
	listener        net.Listener
	keystore        *identity.Keystore
	registry        *services.Registry
	registryPtr     atomic.Pointer[services.Registry]
	sigServer       *signaling.Server
	torEngine       *tor.Engine
	torCancel       context.CancelFunc
	p2pHost         *transport.Host
	connector       *transport.Connector
	chatService     *chat.Service
	ftSender        *filetransfer.Sender
	ftReceiver      *filetransfer.Receiver
	nodeHandlers    *nodeHandlers
	presence        *transport.Presence
	presenceCancel  context.CancelFunc
	reconnectCancel context.CancelFunc
}

// New creates a daemon with the given config.
func New(cfg Config) *Daemon {
	return &Daemon{cfg: cfg}
}

// Start initializes identity, starts the node, and listens for gRPC connections.
func (d *Daemon) Start() error {
	if err := os.MkdirAll(d.cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	kp, err := d.loadOrGenerateIdentity()
	if err != nil {
		return fmt.Errorf("setting up identity: %w", err)
	}

	d.node = node.New(kp)
	addr := d.node.Address()
	log.Printf("identity: %s", addr)

	// Initialize contacts store.
	cs := contacts.NewStore(d.cfg.DataDir)
	if err := cs.Load(); err != nil {
		log.Printf("contacts: fresh store (load: %v)", err)
	}
	d.node.SetContacts(cs)

	// Start libp2p host for direct connections.
	if !d.cfg.DisableP2P {
		h, err := transport.NewHost(transport.HostConfig{
			Keypair:    kp,
			ListenPort: d.cfg.P2PPort,
		})
		if err != nil {
			log.Printf("p2p: failed to start host: %v", err)
		} else {
			d.p2pHost = h
			d.node.SetHost(h)
			log.Printf("p2p: listening on %v", h.Addrs())
		}
	}

	// Start chat service if p2p host is available.
	if d.p2pHost != nil {
		history := chat.NewHistory()
		chatSvc := chat.NewService(d.p2pHost, d.node.EventBus(), &nodeResolver{node: d.node}, history, &daemonServiceLookup{d: d}, identity.NodeService)
		d.chatService = chatSvc
		d.node.SetSendMessage(func(ctx context.Context, addr, text string) (string, error) {
			msg, err := chatSvc.SendMessage(ctx, addr, text)
			if err != nil {
				return "", err
			}
			return msg.ID, nil
		})
		log.Printf("chat: service started")

		// Start file transfer sender and receiver. Both resolve their
		// owning service through the registry at dispatch time, mirroring
		// the chat migration in T05. The lookup pointer is shared with the
		// chat service.
		saveDir := filepath.Join(d.cfg.DataDir, "received")
		ftLookup := &daemonServiceLookup{d: d}
		ftSender := filetransfer.NewSender(d.p2pHost, &nodeResolver{node: d.node}, ftLookup, identity.NodeService)
		ftReceiver := filetransfer.NewReceiver(d.p2pHost, d.node.EventBus(), saveDir, ftLookup, identity.NodeService)
		d.ftSender = ftSender
		d.ftReceiver = ftReceiver
		d.nodeHandlers = &nodeHandlers{Service: chatSvc, Receiver: ftReceiver}

		d.node.SetSendFile(func(ctx context.Context, peerAddr, filePath string) (string, error) {
			xfer, err := ftSender.OfferFile(ctx, peerAddr, filePath)
			if err != nil {
				return "", err
			}
			return xfer.ID, nil
		})
		d.node.SetAcceptFile(func(transferID, savePath string) error {
			return ftReceiver.AcceptFile(transferID, savePath)
		})
		d.node.SetRejectFile(func(transferID, reason string) error {
			return ftReceiver.RejectFile(transferID, reason)
		})
		log.Printf("filetransfer: service started")

		// Start presence pinger.
		pres := transport.NewPresence(d.p2pHost, kp)
		d.presence = pres
		presCtx, presCancel := context.WithCancel(context.Background())
		d.presenceCancel = presCancel
		go pres.Start(presCtx, &nodeConnectorLister{node: d.node}, func(s transport.OnlineStatus) {
			d.node.EventBus().Publish(node.Event{
				Type:    node.EventPeerOnline,
				Payload: s,
			})
		})
		log.Printf("presence: pinger started")
	}

	// Start Tor in the background.
	if !d.cfg.DisableTor {
		d.startTor()
	}

	var grpcOpts []grpc.ServerOption
	if d.cfg.AdminKey != "" {
		adminPub, err := hex.DecodeString(d.cfg.AdminKey)
		if err != nil || len(adminPub) != 32 {
			return fmt.Errorf("invalid admin key: must be 64 hex chars (32 bytes Ed25519 public key)")
		}
		grpcOpts = append(grpcOpts,
			grpc.UnaryInterceptor(api.AdminKeyUnaryInterceptor(adminPub)),
			grpc.StreamInterceptor(api.AdminKeyStreamInterceptor(adminPub)),
		)
		log.Printf("gRPC Ed25519 authentication enabled (admin=%s...)", d.cfg.AdminKey[:16])
	}
	d.grpcServer = grpc.NewServer(grpcOpts...)
	d.apiServer = api.NewServer(d.node)
	d.apiServer.SetChatDispatcher(&nodeChatDispatcher{node: d.node})
	d.apiServer.Register(d.grpcServer)

	if err := d.listen(); err != nil {
		return fmt.Errorf("starting listener: %w", err)
	}

	go d.grpcServer.Serve(d.listener)
	log.Printf("gRPC server listening")

	return nil
}

// startTor launches the Tor engine and bridges events to the node.
func (d *Daemon) startTor() {
	ctx, cancel := context.WithCancel(context.Background())
	d.torCancel = cancel

	d.torEngine = tor.NewEngine(d.cfg.DataDir, d.cfg.TorPath)
	d.node.SetTorState("bootstrapping")

	if err := d.torEngine.Start(ctx); err != nil {
		log.Printf("tor: failed to start: %v", err)
		d.node.SetTorState("disabled")
		return
	}

	// Bridge bootstrap progress events to the node's event bus.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-d.torEngine.ProgressChan():
				if !ok {
					return
				}
				d.node.EventBus().Publish(node.Event{
					Type:    node.EventTorBootstrap,
					Payload: evt,
				})
			}
		}
	}()

	// Wait for Tor to be ready, then provision the node service via the registry.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-d.torEngine.Ready():
		}

		log.Printf("tor: bootstrap complete, SOCKS at %s", d.torEngine.SOCKSAddr())

		d.registry = services.New(d.keystore, d.torEngine)
		d.registryPtr.Store(d.registry)
		if d.apiServer != nil {
			d.apiServer.SetRegistry(d.registry)
		}

		nodeSvc, err := d.registry.Register(ctx, identity.NodeService, services.Manifest{
			Description: "primary chat + filetransfer service",
			ACL:         services.ACLContacts,
		}, d.nodeHandlers)
		if err != nil {
			log.Printf("tor: failed to register node service: %v", err)
			d.node.SetTorState("ready")
			d.node.EventBus().Publish(node.Event{Type: node.EventTorReady})
			return
		}
		ln, ok := d.torEngine.OnionListener(identity.NodeService)
		if !ok {
			log.Printf("tor: node onion listener missing after Register")
			d.node.SetTorState("ready")
			d.node.EventBus().Publish(node.Event{Type: node.EventTorReady})
			return
		}

		d.node.SetOnionAddr(nodeSvc.OnionAddr)
		d.node.SetTorState("ready")
		log.Printf("tor: hidden service at %s", nodeSvc.OnionAddr)

		d.node.EventBus().Publish(node.Event{
			Type:    node.EventTorReady,
			Payload: nodeSvc.OnionAddr,
		})

		// Wire discovery, signaling, and connector now that the node service is live.
		d.wireSubsystems(ctx, nodeSvc, ln)
	}()
}

// Stop gracefully shuts down the daemon.
func (d *Daemon) Stop() {
	if d.reconnectCancel != nil {
		d.reconnectCancel()
	}
	if d.presenceCancel != nil {
		d.presenceCancel()
	}
	if d.presence != nil {
		d.presence.Close()
	}
	if d.ftReceiver != nil {
		d.ftReceiver.Close()
	}
	if d.chatService != nil {
		d.chatService.Close()
	}
	if d.sigServer != nil {
		d.sigServer.Stop()
	}
	if d.grpcServer != nil {
		d.grpcServer.GracefulStop()
	}
	if d.listener != nil {
		d.listener.Close()
	}
	if d.p2pHost != nil {
		d.p2pHost.Close()
	}
	if d.torCancel != nil {
		d.torCancel()
	}
	if d.torEngine != nil {
		d.torEngine.Stop()
	}
	// Clean up Unix socket file.
	if d.cfg.TCPAddr == "" {
		os.Remove(d.cfg.SocketPath)
	}
}

// wireSubsystems initializes discovery, signaling, and the connection orchestrator.
// Called once Tor is ready and the node service is registered.
func (d *Daemon) wireSubsystems(ctx context.Context, nodeSvc *services.Service, nodeLn net.Listener) {
	kp := d.node.Identity()
	addr := string(d.node.Address())
	cs := d.node.Contacts()
	onionAddr := nodeSvc.OnionAddr

	// Create Tor dialer adapter.
	torDialer, err := d.torEngine.Dialer(ctx)
	if err != nil {
		log.Printf("subsystems: failed to create tor dialer: %v", err)
		return
	}
	dialAdapter := &torDialerAdapter{dialer: torDialer, port: tor.OnionServicePort}

	// Discovery: routing table + DHT + mDNS.
	localID := discovery.NodeIDFromPublicKey(kp.PublicKey())
	rtPath := filepath.Join(d.cfg.DataDir, "routing_table.json")
	rt := discovery.NewRoutingTable(localID)
	if err := rt.Load(rtPath); err != nil {
		log.Printf("discovery: fresh routing table (load: %v)", err)
	}
	localPeer := &discovery.PeerInfo{
		ID:        localID,
		Address:   addr,
		OnionAddr: onionAddr,
	}
	dht := discovery.NewDHTDiscovery(rt, localPeer, dialAdapter)
	mdns := discovery.NewMDNSDiscovery(addr, onionAddr)
	discMgr := discovery.NewManager(dht, mdns)
	discMgr.SetRTPath(rtPath)
	d.node.SetDiscovery(discMgr)
	log.Printf("discovery: DHT + mDNS initialized (rt_size=%d)", rt.Size())

	// Signaling server: per-service demux, with the DHT plumbed in as the
	// fallback envelope handler so each per-service onion still routes
	// DHT FIND_NODE / PUT_RECORD off the same listener.
	sigServer := signaling.NewServer()
	sigServer.SetEnvelopeHandler(func(c net.Conn, env *pb.Envelope) {
		switch env.Type {
		case pb.MessageType_DHT_FIND_NODE, pb.MessageType_DHT_PUT_RECORD:
			defer c.Close()
			dht.HandleEnvelope(c, env)
		default:
			c.Close()
		}
	})
	if err := sigServer.AddService(signaling.ServiceConfig{
		Name:       nodeSvc.Name,
		Keypair:    kp,
		Address:    addr,
		Contacts:   cs,
		Listener:   nodeLn,
		OnRequest:  d.node.HandleConnectionRequest,
		LocalAddrs: d.localAddrs,
	}); err != nil {
		log.Printf("signaling: AddService(%s): %v", nodeSvc.Name, err)
		return
	}
	if err := sigServer.Start(ctx); err != nil {
		log.Printf("signaling: Start: %v", err)
		return
	}
	d.sigServer = sigServer
	sigClient := signaling.NewClient(kp, addr, dialAdapter)
	d.node.SetSignaling(sigServer, sigClient)
	log.Printf("signaling: per-service demux started (services=%s)", nodeSvc.Name)

	// Announce to DHT if routing table has peers from disk.
	if rt.Size() > 0 {
		go func() {
			if err := dht.Announce(ctx); err != nil {
				log.Printf("discovery: startup announce: %v", err)
			} else {
				log.Printf("discovery: startup announce succeeded (rt_size=%d)", rt.Size())
			}
			if saveErr := rt.Save(rtPath); saveErr != nil {
				log.Printf("discovery: save routing table: %v", saveErr)
			}
		}()
	}

	// Start periodic re-announce loop.
	discMgr.StartAnnounceLoop(ctx)

	// Connection orchestrator (only if p2p host is available).
	if d.p2pHost != nil {
		conn := transport.NewConnector(transport.ConnectorConfig{
			Keypair: kp,
			Host:    d.p2pHost,
			Finder:  &discoveryFinderAdapter{mgr: discMgr},
			Dialer:  dialAdapter,
			OnState: func(e transport.StateEvent) {
				d.node.EventBus().Publish(node.Event{
					Type:    node.EventConnectionState,
					Payload: e,
				})
			},
		})
		d.connector = conn
		d.node.SetConnector(conn)
		log.Printf("connector: connection orchestrator initialized")

		// Start auto-reconnect if presence service is running.
		if d.presence != nil {
			reconnCtx, reconnCancel := context.WithCancel(context.Background())
			d.reconnectCancel = reconnCancel
			go conn.StartAutoReconnect(reconnCtx, d.presence)
			log.Printf("reconnect: auto-reconnection started")
		}
	}
}

// Node returns the underlying node (for in-process TUI access).
func (d *Daemon) Node() *node.Node {
	return d.node
}

// loadOrGenerateIdentity opens (or initializes) the daemon's keystore and
// loads-or-generates the keypair for the primary "node" service. The keystore
// is retained on the Daemon so the services registry can reuse it.
func (d *Daemon) loadOrGenerateIdentity() (*identity.Keypair, error) {
	ks, err := identity.NewKeystore(d.cfg.DataDir, d.cfg.Passphrase)
	if err != nil {
		return nil, fmt.Errorf("opening keystore: %w", err)
	}
	d.keystore = ks

	had := ks.Has(identity.NodeService)
	kp, err := ks.GetOrGenerate(identity.NodeService)
	if err != nil {
		return nil, fmt.Errorf("loading node identity: %w", err)
	}
	if had {
		log.Printf("loaded existing identity")
	} else {
		log.Printf("generated new identity")
	}
	return kp, nil
}

// localAddrs returns the daemon's libp2p multiaddrs as strings, suitable for
// the responder-side IP exchange in signaling.ServiceConfig.LocalAddrs. Empty
// if the libp2p host is disabled.
func (d *Daemon) localAddrs() []string {
	if d.p2pHost == nil {
		return nil
	}
	return d.p2pHost.Addrs()
}

// daemonServiceLookup is the chat.ServiceLookup / filetransfer.ServiceLookup
// that built-in services hold at construction time. The underlying registry
// is created later in startTor's goroutine, so this indirection lets
// chat.NewService and filetransfer.New{Sender,Receiver} run during Start()
// before Tor is ready. Lookups before the registry is wired up return
// (nil, false), which is safe — no chat or filetransfer traffic flows
// before the registered onion service is live. The registry pointer is
// read through an atomic so it is race-clean against the startTor
// goroutine that publishes it.
type daemonServiceLookup struct {
	d *Daemon
}

func (l *daemonServiceLookup) Get(name string) (*services.Service, bool) {
	if l.d == nil {
		return nil, false
	}
	r := l.d.registryPtr.Load()
	if r == nil {
		return nil, false
	}
	return r.Get(name)
}

// nodeChatDispatcher routes outbound chat from gRPC-registered services
// through the daemon's existing chat service. v0 limitation: messages travel
// under the daemon's primary node identity, not the registered service's
// identity. A future ticket should plumb a per-service chat path so on-wire
// "from" addresses match the registered service. Documented in
// planning/multi-service-platform/progress.md (T07).
type nodeChatDispatcher struct {
	node *node.Node
}

func (d *nodeChatDispatcher) Send(ctx context.Context, _, toAddr, text string) (string, error) {
	return d.node.SendMessage(ctx, toAddr, text)
}

// nodeConnectorLister adapts the node to the transport.PeerLister interface.
type nodeConnectorLister struct {
	node *node.Node
}

func (l *nodeConnectorLister) ActivePeers() []*transport.PeerConnection {
	return l.node.ActiveConnections()
}

// nodeResolver adapts the node's Connector to the chat.PeerResolver interface.
type nodeResolver struct {
	node *node.Node
}

func (r *nodeResolver) GetPeer(addr string) *transport.PeerConnection {
	return r.node.ConnectionInfo(addr)
}

// torDialerAdapter wraps tor.Dialer to implement the DialContext interface
// expected by discovery, signaling, and transport packages.
type torDialerAdapter struct {
	dialer *tor.Dialer
	port   int
}

func (a *torDialerAdapter) DialContext(ctx context.Context, address string) (net.Conn, error) {
	return a.dialer.DialOnion(ctx, address, a.port)
}

// discoveryFinderAdapter bridges discovery.Manager to transport.PeerFinder.
type discoveryFinderAdapter struct {
	mgr *discovery.Manager
}

func (a *discoveryFinderAdapter) FindPeer(ctx context.Context, addr string) (transport.PeerLookupResult, error) {
	info, err := a.mgr.FindPeer(ctx, addr)
	if err != nil {
		return transport.PeerLookupResult{}, err
	}
	return transport.PeerLookupResult{
		Address:   info.Address,
		OnionAddr: info.OnionAddr,
	}, nil
}

// listen starts the appropriate network listener.
func (d *Daemon) listen() error {
	// TCP mode (for headless/remote).
	if d.cfg.TCPAddr != "" {
		lis, err := net.Listen("tcp", d.cfg.TCPAddr)
		if err != nil {
			return fmt.Errorf("listening on TCP %s: %w", d.cfg.TCPAddr, err)
		}
		d.listener = lis
		log.Printf("listening on TCP %s", d.cfg.TCPAddr)
		return nil
	}

	// Unix socket mode (default).
	os.Remove(d.cfg.SocketPath) // remove stale socket
	lis, err := net.Listen("unix", d.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listening on socket %s: %w", d.cfg.SocketPath, err)
	}
	d.listener = lis
	log.Printf("listening on socket %s", d.cfg.SocketPath)
	return nil
}
