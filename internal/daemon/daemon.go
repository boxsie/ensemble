package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/boxsie/ensemble/internal/api"
	"github.com/boxsie/ensemble/internal/chat"
	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/discovery"
	"github.com/boxsie/ensemble/internal/filetransfer"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/tor"
	"github.com/boxsie/ensemble/internal/transport"
	"google.golang.org/grpc"
)

const onionServicePort = 9735

// Daemon manages the node lifecycle and gRPC server.
type Daemon struct {
	cfg          Config
	node         *node.Node
	grpcServer   *grpc.Server
	listener     net.Listener
	torEngine    *tor.Engine
	onionService *tor.OnionService
	torCancel    context.CancelFunc
	p2pHost          *transport.Host
	connector        *transport.Connector
	chatService      *chat.Service
	ftSender         *filetransfer.Sender
	ftReceiver       *filetransfer.Receiver
	presence         *transport.Presence
	presenceCancel   context.CancelFunc
	reconnectCancel  context.CancelFunc
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
		chatSvc := chat.NewService(kp, d.p2pHost, d.node.EventBus(), &nodeResolver{node: d.node}, history)
		d.chatService = chatSvc
		d.node.SetSendMessage(func(ctx context.Context, addr, text string) (string, error) {
			msg, err := chatSvc.SendMessage(ctx, addr, text)
			if err != nil {
				return "", err
			}
			return msg.ID, nil
		})
		log.Printf("chat: service started")

		// Start file transfer sender and receiver.
		saveDir := filepath.Join(d.cfg.DataDir, "received")
		ftSender := filetransfer.NewSender(kp, d.p2pHost, &nodeResolver{node: d.node})
		ftReceiver := filetransfer.NewReceiver(kp, d.p2pHost, d.node.EventBus(), saveDir)
		d.ftSender = ftSender
		d.ftReceiver = ftReceiver

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

	d.grpcServer = grpc.NewServer()
	apiServer := api.NewServer(d.node)
	apiServer.Register(d.grpcServer)

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

	// Wait for Tor to be ready, then create the hidden service.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-d.torEngine.Ready():
		}

		log.Printf("tor: bootstrap complete, SOCKS at %s", d.torEngine.SOCKSAddr())

		keyPath := filepath.Join(d.cfg.DataDir, "onion.pem")
		svc, err := d.torEngine.CreateOnionService(ctx, onionServicePort, keyPath)
		if err != nil {
			log.Printf("tor: failed to create onion service: %v", err)
			d.node.SetTorState("ready")
			d.node.EventBus().Publish(node.Event{Type: node.EventTorReady})
			return
		}

		d.onionService = svc
		d.node.SetOnionAddr(svc.OnionAddr())
		d.node.SetTorState("ready")
		log.Printf("tor: hidden service at %s", svc.OnionAddr())

		d.node.EventBus().Publish(node.Event{
			Type:    node.EventTorReady,
			Payload: svc.OnionAddr(),
		})

		// Wire discovery, signaling, and connector now that Tor is ready.
		d.wireSubsystems(ctx, svc.OnionAddr())
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
	if d.grpcServer != nil {
		d.grpcServer.GracefulStop()
	}
	if d.listener != nil {
		d.listener.Close()
	}
	if d.p2pHost != nil {
		d.p2pHost.Close()
	}
	if d.onionService != nil {
		d.onionService.Close()
	}
	if d.torEngine != nil {
		d.torEngine.Stop()
	}
	if d.torCancel != nil {
		d.torCancel()
	}
	// Clean up Unix socket file.
	if d.cfg.TCPAddr == "" {
		os.Remove(d.cfg.SocketPath)
	}
}

// wireSubsystems initializes discovery, signaling, and the connection orchestrator.
// Called once Tor is ready and the onion service is published.
func (d *Daemon) wireSubsystems(ctx context.Context, onionAddr string) {
	kp := d.node.Identity()
	addr := string(d.node.Address())
	cs := d.node.Contacts()

	// Create Tor dialer adapter.
	torDialer, err := d.torEngine.Dialer(ctx)
	if err != nil {
		log.Printf("subsystems: failed to create tor dialer: %v", err)
		return
	}
	dialAdapter := &torDialerAdapter{dialer: torDialer, port: onionServicePort}

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
	d.node.SetDiscovery(discMgr)
	log.Printf("discovery: DHT + mDNS initialized")

	// Signaling server + client.
	sigServer := signaling.NewServer(kp, addr, cs)
	sigClient := signaling.NewClient(kp, addr, dialAdapter)
	d.node.SetSignaling(sigServer, sigClient)

	// Start signaling server on the onion service listener.
	if d.onionService != nil {
		go sigServer.Serve(ctx, d.onionService.Listener())
		log.Printf("signaling: server started on onion service")
	}

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

// loadOrGenerateIdentity loads an existing keypair or generates a new one.
func (d *Daemon) loadOrGenerateIdentity() (*identity.Keypair, error) {
	ks := identity.NewKeystore(d.cfg.DataDir)

	if ks.Exists() {
		log.Printf("loading existing identity")
		return ks.Load(d.cfg.Passphrase)
	}

	log.Printf("generating new identity")
	kp, err := identity.Generate()
	if err != nil {
		return nil, err
	}

	if err := ks.Save(kp, d.cfg.Passphrase); err != nil {
		return nil, fmt.Errorf("saving identity: %w", err)
	}

	return kp, nil
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
