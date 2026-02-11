package signaling

import (
	"context"
	"fmt"
	"net"
	"sync"

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
type ConnectionRequest struct {
	FromAddress string
	Decision    chan ConnectionDecision
}

// Server listens for inbound signaling connections on a Tor hidden service.
// It implements contact-gated handshake: known contacts auto-proceed,
// unknown addresses require explicit user acceptance.
type Server struct {
	keypair  *identity.Keypair
	address  string // our ensemble address
	contacts *contacts.Store
	nc       *NonceCache

	// OnConnectionRequest is called when an unknown peer requests connection.
	// The caller must send a decision on the ConnectionRequest.Decision channel.
	onRequest func(*ConnectionRequest)
	mu        sync.Mutex

	listener net.Listener
	cancel   context.CancelFunc
}

// NewServer creates a signaling server.
func NewServer(kp *identity.Keypair, addr string, cs *contacts.Store) *Server {
	return &Server{
		keypair:  kp,
		address:  addr,
		contacts: cs,
		nc:       NewNonceCache(),
	}
}

// OnConnectionRequest sets the callback for unknown connection requests.
func (s *Server) OnConnectionRequest(cb func(*ConnectionRequest)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRequest = cb
}

// Serve starts accepting connections on the given listener.
// Blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.listener = ln
	s.cancel = cancel
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accepting connection: %w", err)
			}
		}
		go s.handleConn(ctx, conn)
	}
}

// Stop shuts down the server.
func (s *Server) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	ln := s.listener
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ln != nil {
		ln.Close()
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Step 1: Read ConnRequest.
	env, err := protocol.ReadMsg(conn)
	if err != nil {
		return
	}
	if env.Type != pb.MessageType_CONN_REQUEST {
		return
	}

	req, err := ValidateConnRequest(env.Payload, s.nc)
	if err != nil {
		return
	}

	// Verify this request is for us.
	if req.TargetAddress != s.address {
		return
	}

	// Contact gating: check if the initiator is a known contact.
	accepted := false
	reason := ""

	if c := s.contacts.Get(req.FromAddress); c != nil {
		// Known contact — auto-accept.
		accepted = true
	} else {
		// Unknown — ask user.
		decision := s.askUser(req.FromAddress, ctx)
		accepted = decision.Accepted
		reason = decision.Reason
	}

	// Step 2: Send ConnResponse.
	var respNonce []byte
	if accepted {
		var respData []byte
		respData, respNonce, err = CreateConnResponse(true, s.keypair, req.Nonce, "")
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
		return // rejected — done
	}

	// Step 3: Read ConnConfirm.
	confirmEnv, err := protocol.ReadMsg(conn)
	if err != nil {
		return
	}
	if confirmEnv.Type != pb.MessageType_CONN_CONFIRM {
		return
	}

	_, err = ValidateConnConfirm(confirmEnv.Payload, respNonce, req.FromAddress)
	if err != nil {
		return
	}

	// Handshake complete — both sides verified.
}

func (s *Server) askUser(fromAddr string, ctx context.Context) ConnectionDecision {
	s.mu.Lock()
	cb := s.onRequest
	s.mu.Unlock()

	if cb == nil {
		return ConnectionDecision{Accepted: false, Reason: "no handler"}
	}

	cr := &ConnectionRequest{
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
