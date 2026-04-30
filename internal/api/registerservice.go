package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	apipb "github.com/boxsie/ensemble/api/pb"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/services"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// eventQueueSize bounds the per-stream queue between the registry handler and
// the gRPC stream. When the queue fills, the oldest event is dropped to keep
// the stream alive at the cost of stale data on slow clients.
const eventQueueSize = 256

// ChatDispatcher is the seam through which a registered service's outbound
// chat goes. The daemon supplies an implementation that translates a service
// name + recipient address + text into the chat service's send pipeline.
//
// v0 scope: the implementation may dispatch using the daemon's primary chat
// service (i.e. messages travel under the node's identity, not the registered
// service's). External services see a working SendMessage but the on-wire
// "from" address is the node's. T05 / future tickets are expected to wire a
// per-service chat path so messages travel under the registered service's
// identity.
type ChatDispatcher interface {
	Send(ctx context.Context, fromService, toAddr, text string) (string, error)
}

// connectionDecision is the per-stream decision form for an inbound
// connection request. The registered service's gRPC client surfaces it via
// ServiceAcceptConnection / ServiceRejectConnection messages keyed by
// request_id.
type connectionDecision struct {
	Accepted bool
	Reason   string
}

// PendingRequestStore lets the RegisterService handler park inbound
// connection-request decisions keyed by the request_id surfaced to the client.
// The default implementation is in-memory and lives on the api.Server.
type pendingRequestStore struct {
	mu      sync.Mutex
	pending map[string]chan connectionDecision
}

func newPendingRequestStore() *pendingRequestStore {
	return &pendingRequestStore{pending: make(map[string]chan connectionDecision)}
}

func (p *pendingRequestStore) put(id string, ch chan connectionDecision) {
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
}

func (p *pendingRequestStore) take(id string) (chan connectionDecision, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch, ok := p.pending[id]
	if !ok {
		return nil, false
	}
	delete(p.pending, id)
	return ch, true
}

// dropAll cancels every pending request with a "stream closed" rejection.
// Called when the gRPC stream for a service goes away.
func (p *pendingRequestStore) dropAll(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, ch := range p.pending {
		select {
		case ch <- connectionDecision{Accepted: false, Reason: reason}:
		default:
		}
		delete(p.pending, id)
	}
}

// registerStream is the per-call state for one RegisterService bidi stream.
type registerStream struct {
	stream  apipb.EnsembleService_RegisterServiceServer
	events  chan *apipb.ServiceServerMessage
	pending *pendingRequestStore

	// sendMu serialises writes to the gRPC stream. The grpc-go stream API is
	// safe for one writer at a time; the action goroutine sends ServiceError
	// replies and the event-forwarder goroutine sends events, so we guard.
	sendMu sync.Mutex

	dropCount int
}

// RegisterService implements the bidirectional RegisterService RPC. The first
// client message must be a manifest; subsequent messages are actions
// (SendMessage / Accept / Reject). The server streams ServiceEvent messages
// for inbound chat / connection-request events, plus ServiceError for
// recoverable handler-side errors.
//
// On stream close (client disconnect, network error, ctx cancel), the service
// is unregistered. The keypair is retained in the keystore so a reconnect
// under the same name yields the same address.
func (s *Server) RegisterService(stream apipb.EnsembleService_RegisterServiceServer) error {
	if s.registry == nil {
		return status.Error(codes.Unimplemented, "service registry not configured on this daemon")
	}

	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	manifestMsg, ok := first.GetMsg().(*apipb.ServiceClientMessage_Manifest)
	if !ok || manifestMsg.Manifest == nil {
		_ = sendError(stream, "first message must be a ServiceManifest")
		return status.Error(codes.InvalidArgument, "first message must be a ServiceManifest")
	}
	manifest := manifestMsg.Manifest

	if err := validateManifest(manifest); err != nil {
		_ = sendError(stream, err.Error())
		return status.Error(codes.InvalidArgument, err.Error())
	}

	rs := &registerStream{
		stream:  stream,
		events:  make(chan *apipb.ServiceServerMessage, eventQueueSize),
		pending: newPendingRequestStore(),
	}

	handlers := &serviceHandlers{
		serviceName: manifest.Name,
		rs:          rs,
		dispatcher:  s.chatDispatcher,
	}

	svc, err := s.registry.Register(ctx, manifest.Name, services.Manifest{
		Description: manifest.Description,
		ACL:         aclFromProto(manifest.Acl),
		Allowlist:   append([]string(nil), manifest.Allowlist...),
	}, handlers)
	if err != nil {
		_ = sendError(stream, fmt.Sprintf("registering service: %v", err))
		if errors.Is(err, services.ErrAlreadyRegistered) {
			return status.Error(codes.AlreadyExists, err.Error())
		}
		return status.Errorf(codes.Internal, "registering service: %v", err)
	}
	defer func() {
		if uerr := s.registry.Unregister(manifest.Name); uerr != nil && !errors.Is(uerr, services.ErrNotRegistered) {
			log.Printf("registerservice[%s]: unregister on disconnect: %v", manifest.Name, uerr)
		}
		rs.pending.dropAll("service stream closed")
	}()

	if err := rs.send(&apipb.ServiceServerMessage{
		Msg: &apipb.ServiceServerMessage_Registered{
			Registered: &apipb.ServiceRegistered{
				Address: svc.Address,
				Onion:   svc.OnionAddr,
			},
		},
	}); err != nil {
		return err
	}

	// Start the event-forwarder goroutine. It reads from rs.events and writes
	// to the gRPC stream until the action loop returns or the queue closes.
	forwardErr := make(chan error, 1)
	go func() {
		forwardErr <- rs.forwardEvents(ctx)
	}()

	// Action loop runs in this goroutine. Returning here triggers cleanup.
	actionErr := rs.runActions(ctx, s.chatDispatcher, manifest.Name)

	// Closing the event channel lets the forwarder exit cleanly.
	close(rs.events)
	<-forwardErr

	if rs.dropCount > 0 {
		log.Printf("registerservice[%s]: dropped %d events under backpressure", manifest.Name, rs.dropCount)
	}
	return actionErr
}

// runActions reads ServiceClientMessages from the stream and dispatches each
// one. Returns nil on clean EOF, ctx cancellation, or stream error.
func (rs *registerStream) runActions(ctx context.Context, dispatcher ChatDispatcher, serviceName string) error {
	for {
		msg, err := rs.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		switch v := msg.GetMsg().(type) {
		case *apipb.ServiceClientMessage_Manifest:
			_ = rs.sendError("manifest may only be sent once, at the start of the stream")
		case *apipb.ServiceClientMessage_SendMessage:
			rs.handleSend(ctx, dispatcher, serviceName, v.SendMessage)
		case *apipb.ServiceClientMessage_Accept:
			rs.handleAccept(v.Accept)
		case *apipb.ServiceClientMessage_Reject:
			rs.handleReject(v.Reject)
		default:
			_ = rs.sendError("unknown ServiceClientMessage variant")
		}
	}
}

func (rs *registerStream) handleSend(ctx context.Context, dispatcher ChatDispatcher, serviceName string, m *apipb.ServiceSendMessage) {
	if m == nil {
		_ = rs.sendError("send_message payload missing")
		return
	}
	if m.ToAddr == "" || m.Text == "" {
		_ = rs.sendError("send_message requires to_addr and text")
		return
	}
	if dispatcher == nil {
		_ = rs.sendError("chat dispatcher not configured on this daemon")
		return
	}
	if _, err := dispatcher.Send(ctx, serviceName, m.ToAddr, m.Text); err != nil {
		_ = rs.sendError(fmt.Sprintf("send_message: %v", err))
	}
}

func (rs *registerStream) handleAccept(m *apipb.ServiceAcceptConnection) {
	if m == nil || m.RequestId == "" {
		_ = rs.sendError("accept requires request_id")
		return
	}
	ch, ok := rs.pending.take(m.RequestId)
	if !ok {
		_ = rs.sendError(fmt.Sprintf("no pending connection request %q", m.RequestId))
		return
	}
	select {
	case ch <- connectionDecision{Accepted: true}:
	default:
		_ = rs.sendError(fmt.Sprintf("connection request %q already decided", m.RequestId))
	}
}

func (rs *registerStream) handleReject(m *apipb.ServiceRejectConnection) {
	if m == nil || m.RequestId == "" {
		_ = rs.sendError("reject requires request_id")
		return
	}
	ch, ok := rs.pending.take(m.RequestId)
	if !ok {
		_ = rs.sendError(fmt.Sprintf("no pending connection request %q", m.RequestId))
		return
	}
	select {
	case ch <- connectionDecision{Accepted: false, Reason: m.Reason}:
	default:
		_ = rs.sendError(fmt.Sprintf("connection request %q already decided", m.RequestId))
	}
}

// forwardEvents drains rs.events and writes each one to the gRPC stream.
// Returns when ctx is cancelled, the channel is closed, or stream.Send errors.
func (rs *registerStream) forwardEvents(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-rs.events:
			if !ok {
				return nil
			}
			if err := rs.send(ev); err != nil {
				return err
			}
		}
	}
}

func (rs *registerStream) send(m *apipb.ServiceServerMessage) error {
	rs.sendMu.Lock()
	defer rs.sendMu.Unlock()
	return rs.stream.Send(m)
}

func (rs *registerStream) sendError(text string) error {
	return rs.send(&apipb.ServiceServerMessage{
		Msg: &apipb.ServiceServerMessage_Error{
			Error: &apipb.ServiceError{Message: text},
		},
	})
}

// emit pushes an event onto the per-stream queue. If the queue is full, the
// oldest event is dropped and rs.dropCount is incremented. We never block the
// caller because the caller is the registry's per-service handler goroutine
// and stalling it would back-pressure the entire signaling path.
func (rs *registerStream) emit(ev *apipb.ServiceServerMessage) {
	for {
		select {
		case rs.events <- ev:
			return
		default:
			// Drop oldest, retry once. If this select also fails we fall
			// through to the next loop iteration; either the channel is
			// closed (we'll panic on the next send and recover via the
			// outer goroutine's exit) or the consumer caught up.
			select {
			case <-rs.events:
				rs.dropCount++
			default:
			}
		}
	}
}

// serviceHandlers is the registry-side handler bundle for one RegisterService
// stream. It implements ChatHandler and ConnectionHandler; events translate
// into ServiceServerMessage instances pushed onto the per-stream queue.
type serviceHandlers struct {
	serviceName string
	rs          *registerStream
	dispatcher  ChatDispatcher

	idMu sync.Mutex
	idN  uint64
}

// newRequestID returns a process-unique id for an inbound connection request.
// Uniqueness across daemon restarts isn't required because pending requests
// are an ephemeral, per-stream concept.
func (h *serviceHandlers) newRequestID(fromAddr string) string {
	h.idMu.Lock()
	h.idN++
	n := h.idN
	h.idMu.Unlock()
	return fmt.Sprintf("%s-%d", fromAddr, n)
}

// OnChatMessage emits a chat_message event onto the per-stream queue.
func (h *serviceHandlers) OnChatMessage(_ context.Context, fromAddr, text string) error {
	payload, err := proto.Marshal(&apipb.ServiceChatMessageEvent{
		FromAddr: fromAddr,
		Text:     text,
		Ts:       time.Now().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("marshalling chat_message: %w", err)
	}
	h.rs.emit(&apipb.ServiceServerMessage{
		Msg: &apipb.ServiceServerMessage_Event{
			Event: &apipb.ServiceEvent{
				Type:    "chat_message",
				Payload: payload,
			},
		},
	})
	return nil
}

// OnConnectionRequest parks the decision channel under a fresh request_id and
// emits a connection_request event. Returns true / false based on the
// client's accept/reject decision (or false on timeout / disconnect).
func (h *serviceHandlers) OnConnectionRequest(ctx context.Context, req services.ConnectionRequest) (bool, error) {
	id := h.newRequestID(req.FromAddr)
	decision := make(chan connectionDecision, 1)
	h.rs.pending.put(id, decision)

	payload, err := proto.Marshal(&apipb.ServiceConnectionRequestEvent{
		RequestId: id,
		FromAddr:  req.FromAddr,
	})
	if err != nil {
		_, _ = h.rs.pending.take(id)
		return false, fmt.Errorf("marshalling connection_request: %w", err)
	}
	h.rs.emit(&apipb.ServiceServerMessage{
		Msg: &apipb.ServiceServerMessage_Event{
			Event: &apipb.ServiceEvent{
				Type:    "connection_request",
				Payload: payload,
			},
		},
	})

	select {
	case d := <-decision:
		if d.Accepted {
			return true, nil
		}
		return false, nil
	case <-ctx.Done():
		_, _ = h.rs.pending.take(id)
		return false, ctx.Err()
	}
}

// validateManifest rejects manifests with bad names, unknown ACL values, or
// malformed allowlist entries. The registry will revalidate the name; this is
// just an early-exit so we don't have to roll back a half-registered service.
func validateManifest(m *apipb.ServiceManifest) error {
	if m == nil {
		return errors.New("manifest is empty")
	}
	if m.Name == "" {
		return errors.New("manifest.name is empty")
	}
	if m.Acl < apipb.ACL_ACL_PUBLIC || m.Acl > apipb.ACL_ACL_ALLOWLIST {
		return fmt.Errorf("manifest.acl invalid: %v", m.Acl)
	}
	if m.Acl == apipb.ACL_ACL_ALLOWLIST {
		for i, addr := range m.Allowlist {
			if !identity.Validate(addr) {
				return fmt.Errorf("manifest.allowlist[%d] %q is not a valid ensemble address", i, addr)
			}
		}
	}
	// keypair_seed is advisory in v0; the keystore is append-only and we
	// don't expose an Import path. If an existing keypair is on disk under
	// this name, it wins. If not, the keystore generates a fresh one — the
	// seed is intentionally ignored. Documented in progress.md.
	return nil
}

func aclFromProto(a apipb.ACL) services.ACL {
	switch a {
	case apipb.ACL_ACL_PUBLIC:
		return services.ACLPublic
	case apipb.ACL_ACL_CONTACTS:
		return services.ACLContacts
	case apipb.ACL_ACL_ALLOWLIST:
		return services.ACLAllowlist
	default:
		return services.ACLPublic
	}
}

func sendError(stream apipb.EnsembleService_RegisterServiceServer, msg string) error {
	return stream.Send(&apipb.ServiceServerMessage{
		Msg: &apipb.ServiceServerMessage_Error{
			Error: &apipb.ServiceError{Message: msg},
		},
	})
}
