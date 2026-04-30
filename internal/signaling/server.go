package signaling

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

// ConnectionDecision represents the user's accept/reject decision for a connection request.
type ConnectionDecision struct {
	Accepted bool
	Reason   string
}

// ConnectionRequest represents an inbound connection request pending user decision.
// Service identifies the per-daemon service that the inbound request landed on.
type ConnectionRequest struct {
	Service     string
	FromAddress string
	Decision    chan ConnectionDecision
}

// ServiceConfig describes one service's signaling state. Each registered service
// has its own keypair, address, contact store, and unknown-peer callback. The
// listener is the per-onion net.Listener (typically from tor.Engine.OnionListener).
//
// LocalAddrs returns this service's libp2p multiaddrs for the responder-side
// IP exchange that runs after the 3-step handshake. If nil, IP exchange is
// skipped and the initiator falls back to Tor-only routing.
type ServiceConfig struct {
	Name       string
	Keypair    *identity.Keypair
	Address    string
	Contacts   *contacts.Store
	OnRequest  func(*ConnectionRequest)
	Listener   net.Listener
	LocalAddrs func() []string
}

// EnvelopeHandler is invoked for envelopes whose MessageType is not handled by
// the signaling layer. Today this is the DHT path; the daemon plugs this in so
// each per-service onion listener still routes DHT FIND_NODE / PUT_RECORD.
type EnvelopeHandler func(conn net.Conn, env *pb.Envelope)

// ErrServiceExists is returned by AddService when the name is already registered.
var ErrServiceExists = errors.New("signaling: service already added")

// ErrServiceNotFound is returned by RemoveService when no such service is registered.
var ErrServiceNotFound = errors.New("signaling: service not found")

// serviceState is the per-service signaling state held by the server.
type serviceState struct {
	cfg       ServiceConfig
	acceptCtx context.Context
	cancel    context.CancelFunc
}

// Server demultiplexes inbound signaling streams across multiple registered
// services. Each service has its own keypair, contact store, and onion
// listener; the server runs one accept goroutine per service so streams never
// cross over between services.
type Server struct {
	nc *NonceCache

	mu       sync.RWMutex
	parent   context.Context
	cancel   context.CancelFunc
	started  bool
	stopped  bool
	services map[string]*serviceState
	other    EnvelopeHandler
}

// NewServer creates a signaling server with no services. Call AddService to
// register each service before (or after) Start.
func NewServer() *Server {
	return &Server{
		nc:       NewNonceCache(),
		services: make(map[string]*serviceState),
	}
}

// SetEnvelopeHandler installs a handler for envelopes whose message type is
// not a signaling message. The daemon uses this to route DHT envelopes off
// the same onion listener.
func (s *Server) SetEnvelopeHandler(h EnvelopeHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.other = h
}

// Start begins running per-service accept loops. Already-registered services
// start immediately; services added later via AddService start as they are
// added. Returns nil on success; subsequent calls are no-ops.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.stopped {
		s.mu.Unlock()
		return errors.New("signaling: server already stopped")
	}
	parent, cancel := context.WithCancel(ctx)
	s.parent = parent
	s.cancel = cancel
	s.started = true

	for _, st := range s.services {
		s.startAcceptLocked(st)
	}
	s.mu.Unlock()
	return nil
}

// AddService registers a service with the server. If the server has been
// started, the service's accept loop launches immediately; otherwise it
// launches when Start is called.
func (s *Server) AddService(cfg ServiceConfig) error {
	if cfg.Name == "" {
		return errors.New("signaling: AddService requires Name")
	}
	if cfg.Keypair == nil {
		return errors.New("signaling: AddService requires Keypair")
	}
	if cfg.Address == "" {
		return errors.New("signaling: AddService requires Address")
	}
	if cfg.Listener == nil {
		return errors.New("signaling: AddService requires Listener")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("signaling: server stopped")
	}
	if _, exists := s.services[cfg.Name]; exists {
		return fmt.Errorf("%w: %s", ErrServiceExists, cfg.Name)
	}
	st := &serviceState{cfg: cfg}
	s.services[cfg.Name] = st
	if s.started {
		s.startAcceptLocked(st)
	}
	return nil
}

// RemoveService cancels the service's accept loop and drops it from the
// server. The underlying listener is not closed by RemoveService — the caller
// (the Tor engine) owns the listener's lifecycle. The accept goroutine exits
// once the caller closes the listener; RemoveService does not wait for that
// to happen.
func (s *Server) RemoveService(name string) error {
	s.mu.Lock()
	st, ok := s.services[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	delete(s.services, name)
	s.mu.Unlock()

	if st.cancel != nil {
		st.cancel()
	}
	return nil
}

// SetOnConnectionRequest replaces the unknown-peer callback for the given
// service. Returns ErrServiceNotFound if the service has not been added.
func (s *Server) SetOnConnectionRequest(name string, cb func(*ConnectionRequest)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.services[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	st.cfg.OnRequest = cb
	return nil
}

// Stop signals every per-service accept loop to exit. Listeners belong to the
// caller (typically the Tor engine, which closes them in its own Stop) and are
// not closed here; the accept goroutines exit when their listener does.
// Stop does not wait for accept goroutines to finish — closing the listener
// is what unblocks them, and that is the caller's responsibility.
func (s *Server) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	for name, st := range s.services {
		if st.cancel != nil {
			st.cancel()
		}
		delete(s.services, name)
	}
	s.mu.Unlock()
}

// startAcceptLocked launches the accept goroutine for st. Must be called with s.mu held.
func (s *Server) startAcceptLocked(st *serviceState) {
	ctx, cancel := context.WithCancel(s.parent)
	st.acceptCtx = ctx
	st.cancel = cancel
	go s.acceptLoop(st)
}

// acceptLoop runs the accept loop for one service. It exits when the
// listener returns an error (typically because the caller closed it).
func (s *Server) acceptLoop(st *serviceState) {
	for {
		conn, err := st.cfg.Listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(st.acceptCtx, st, conn)
	}
}

// handleConn reads the first envelope off conn and dispatches by message type.
// CONN_REQUEST envelopes run the per-service handshake; everything else is
// passed to the configured EnvelopeHandler (the daemon's DHT router).
func (s *Server) handleConn(ctx context.Context, st *serviceState, conn net.Conn) {
	env, err := protocol.ReadMsg(conn)
	if err != nil {
		conn.Close()
		return
	}

	if env.Type == pb.MessageType_CONN_REQUEST {
		defer conn.Close()
		s.handleEnvelopeForService(ctx, conn, env, st)
		return
	}

	s.mu.RLock()
	other := s.other
	s.mu.RUnlock()
	if other == nil {
		conn.Close()
		return
	}
	other(conn, env)
}

// HandleEnvelope processes a pre-read CONN_REQUEST envelope on an existing
// connection, routing it to the named service's handshake state. The daemon's
// per-onion mux uses this when it has already consumed the first envelope.
// Returns silently if the named service is not registered.
func (s *Server) HandleEnvelope(ctx context.Context, conn net.Conn, env *pb.Envelope, svcName string) {
	s.mu.RLock()
	st, ok := s.services[svcName]
	s.mu.RUnlock()
	if !ok {
		log.Printf("signaling: HandleEnvelope for unknown service %q", svcName)
		return
	}
	s.handleEnvelopeForService(ctx, conn, env, st)
}

func (s *Server) handleEnvelopeForService(ctx context.Context, conn net.Conn, env *pb.Envelope, st *serviceState) {
	if env.Type != pb.MessageType_CONN_REQUEST {
		log.Printf("signaling[%s]: ignoring non-CONN_REQUEST envelope (type=%d)", st.cfg.Name, env.Type)
		return
	}

	req, err := ValidateConnRequest(env.Payload, s.nc)
	if err != nil {
		log.Printf("signaling[%s]: invalid CONN_REQUEST: %v", st.cfg.Name, err)
		return
	}

	log.Printf("signaling[%s]: CONN_REQUEST from=%s target=%s", st.cfg.Name, req.FromAddress, req.TargetAddress)

	if req.TargetAddress != st.cfg.Address {
		log.Printf("signaling[%s]: rejecting request not for us (target=%s, us=%s)",
			st.cfg.Name, req.TargetAddress, st.cfg.Address)
		return
	}

	accepted := false
	reason := ""

	if st.cfg.Contacts != nil {
		if c := st.cfg.Contacts.Get(req.FromAddress); c != nil {
			accepted = true
			log.Printf("signaling[%s]: auto-accepting known contact %s", st.cfg.Name, req.FromAddress)
		}
	}
	if !accepted {
		log.Printf("signaling[%s]: asking user about unknown peer %s", st.cfg.Name, req.FromAddress)
		decision := s.askUser(ctx, st, req.FromAddress)
		accepted = decision.Accepted
		reason = decision.Reason
		log.Printf("signaling[%s]: user decision for %s: accepted=%v reason=%q",
			st.cfg.Name, req.FromAddress, accepted, reason)
	}

	var respNonce []byte
	if accepted {
		var respData []byte
		respData, respNonce, err = CreateConnResponse(true, st.cfg.Keypair, req.Nonce, "")
		if err != nil {
			return
		}
		respEnv := wrapUnsigned(pb.MessageType_CONN_RESPONSE, respData)
		if err := protocol.WriteMsg(conn, respEnv); err != nil {
			return
		}
	} else {
		respData, _, err := CreateConnResponse(false, nil, nil, reason)
		if err != nil {
			return
		}
		respEnv := wrapUnsigned(pb.MessageType_CONN_RESPONSE, respData)
		protocol.WriteMsg(conn, respEnv)
		return
	}

	confirmEnv, err := protocol.ReadMsg(conn)
	if err != nil {
		return
	}
	if confirmEnv.Type != pb.MessageType_CONN_CONFIRM {
		return
	}

	confirmResult, err := ValidateConnConfirm(confirmEnv.Payload, respNonce, req.FromAddress)
	if err != nil {
		return
	}

	if st.cfg.LocalAddrs == nil || confirmResult == nil || len(confirmResult.PubKey) == 0 {
		return
	}
	ourInfo := &IPInfo{Addrs: st.cfg.LocalAddrs()}
	ipCtx, ipCancel := context.WithTimeout(ctx, 10*time.Second)
	defer ipCancel()
	if _, err := ExchangeIPs(ipCtx, conn, ourInfo, st.cfg.Keypair, confirmResult.PubKey); err != nil {
		log.Printf("signaling[%s]: IP exchange with %s failed: %v", st.cfg.Name, req.FromAddress, err)
	}
}

func (s *Server) askUser(ctx context.Context, st *serviceState, fromAddr string) ConnectionDecision {
	s.mu.RLock()
	cb := st.cfg.OnRequest
	s.mu.RUnlock()
	if cb == nil {
		return ConnectionDecision{Accepted: false, Reason: "no handler"}
	}

	cr := &ConnectionRequest{
		Service:     st.cfg.Name,
		FromAddress: fromAddr,
		Decision:    make(chan ConnectionDecision, 1),
	}
	cb(cr)

	select {
	case d := <-cr.Decision:
		return d
	case <-ctx.Done():
		return ConnectionDecision{Accepted: false, Reason: "timeout"}
	}
}

func wrapUnsigned(msgType pb.MessageType, payload []byte) *pb.Envelope {
	return &pb.Envelope{
		Type:    msgType,
		Payload: payload,
	}
}
