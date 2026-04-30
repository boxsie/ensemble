package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"google.golang.org/protobuf/proto"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	protoc "github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/services"
	"github.com/boxsie/ensemble/internal/transport"
)

// Direction indicates whether a message was sent or received.
type Direction string

const (
	Outgoing Direction = "outgoing"
	Incoming Direction = "incoming"
)

// Message represents a chat message.
type Message struct {
	ID        string
	From      string
	To        string
	Text      string
	SentAt    time.Time
	AckedAt   *time.Time
	Direction Direction
}

// PeerResolver maps ensemble addresses to connected peer IDs.
type PeerResolver interface {
	GetPeer(addr string) *transport.PeerConnection
}

// ServiceLookup resolves a service by name. *services.Registry satisfies it;
// kept narrow so tests can substitute a stub registry.
type ServiceLookup interface {
	Get(name string) (*services.Service, bool)
}

// StaticLookup returns a ServiceLookup that always resolves the given service
// under its Name. Useful for tests and for harnesses that bypass the full
// services.Registry.
func StaticLookup(svc *services.Service) ServiceLookup {
	return staticLookup{svc: svc}
}

type staticLookup struct {
	svc *services.Service
}

func (s staticLookup) Get(name string) (*services.Service, bool) {
	if s.svc == nil || s.svc.Name != name {
		return nil, false
	}
	return s.svc, true
}

// Service handles sending and receiving chat messages over libp2p streams.
// It implements services.ChatHandler and is intended to be registered as the
// chat handler for the "node" service in the daemon's service registry.
type Service struct {
	host        *transport.Host
	eventBus    *node.EventBus
	resolver    PeerResolver
	history     *History
	lookup      ServiceLookup
	serviceName string
}

// NewService creates a chat service and registers the incoming stream handler.
// The libp2p stream handler resolves the owning service through lookup at
// dispatch time, so the chat service does not need its own keypair — it uses
// whichever keypair is on the registered services.Service. If history is
// non-nil, sent and received messages are automatically recorded.
func NewService(host *transport.Host, bus *node.EventBus, resolver PeerResolver, history *History, lookup ServiceLookup, serviceName string) *Service {
	s := &Service{
		host:        host,
		eventBus:    bus,
		resolver:    resolver,
		history:     history,
		lookup:      lookup,
		serviceName: serviceName,
	}
	host.SetStreamHandler(protocol.ID(protoc.ProtocolChat), s.handleIncoming)
	return s
}

// History returns the service's message history, or nil.
func (s *Service) History() *History {
	return s.history
}

// Close unregisters the stream handler.
func (s *Service) Close() {
	s.host.RemoveStreamHandler(protocol.ID(protoc.ProtocolChat))
}

// resolveService returns the registered services.Service this chat handler
// represents, or an error if the registry has no entry under serviceName.
func (s *Service) resolveService() (*services.Service, error) {
	if s.lookup == nil {
		return nil, fmt.Errorf("chat: no service registry configured")
	}
	svc, ok := s.lookup.Get(s.serviceName)
	if !ok || svc == nil {
		return nil, fmt.Errorf("chat: service %q not registered", s.serviceName)
	}
	return svc, nil
}

// SendMessage sends a text message to a peer and waits for an ACK. The keypair
// used to sign the message is the one registered for the chat service in the
// services registry.
func (s *Service) SendMessage(ctx context.Context, peerAddr string, text string) (*Message, error) {
	svc, err := s.resolveService()
	if err != nil {
		return nil, err
	}
	pc := s.resolver.GetPeer(peerAddr)
	if pc == nil {
		return nil, fmt.Errorf("peer %s not connected", peerAddr)
	}

	return s.sendTo(ctx, svc, pc.PeerID, peerAddr, text)
}

// OnChatMessage satisfies services.ChatHandler. The libp2p stream handler is
// the primary inbound entry point in v0 and carries the wire-level message ID;
// this method exists so the chat.Service is a valid registry handler and to
// allow non-stream callers (e.g. test harness) to push a synthetic message
// into the same event bus / history pipeline.
func (s *Service) OnChatMessage(_ context.Context, fromAddr, text string) error {
	svc, err := s.resolveService()
	if err != nil {
		return err
	}
	now := time.Now()
	s.ingest(&Message{
		ID:        generateID(),
		From:      fromAddr,
		To:        svc.Address,
		Text:      text,
		SentAt:    now,
		AckedAt:   &now,
		Direction: Incoming,
	})
	return nil
}

// ingest records a received message in history and publishes it on the bus.
func (s *Service) ingest(msg *Message) {
	if s.history != nil {
		s.history.Append(msg.From, msg)
	}
	s.eventBus.Publish(node.Event{
		Type:    node.EventChatMessage,
		Payload: msg,
	})
}

// sendTo sends a message to a specific libp2p peer.
func (s *Service) sendTo(ctx context.Context, svc *services.Service, pid peer.ID, peerAddr string, text string) (*Message, error) {
	msgID := generateID()
	kp := svc.Identity

	chatText := &pb.ChatText{Id: msgID, Text: text}
	payload, err := proto.Marshal(chatText)
	if err != nil {
		return nil, fmt.Errorf("marshaling chat text: %w", err)
	}

	env := protoc.WrapMessage(pb.MessageType_CHAT_TEXT, payload, kp)

	stream, err := s.host.OpenStream(ctx, pid, protocol.ID(protoc.ProtocolChat))
	if err != nil {
		return nil, fmt.Errorf("opening stream: %w", err)
	}
	defer stream.Close()

	stream.SetWriteDeadline(time.Now().Add(transport.StreamTimeout))
	if err := protoc.WriteMsg(stream, env); err != nil {
		stream.Reset()
		return nil, fmt.Errorf("sending message: %w", err)
	}

	stream.SetReadDeadline(time.Now().Add(transport.StreamTimeout))
	ackEnv, err := protoc.ReadMsg(stream)
	if err != nil {
		stream.Reset()
		return nil, fmt.Errorf("reading ACK: %w", err)
	}

	if ackEnv.Type != pb.MessageType_CHAT_ACK {
		return nil, fmt.Errorf("unexpected response type: %v", ackEnv.Type)
	}
	if !protoc.VerifyEnvelope(ackEnv) {
		return nil, fmt.Errorf("invalid ACK signature")
	}

	ack := &pb.ChatAck{}
	if err := proto.Unmarshal(ackEnv.Payload, ack); err != nil {
		return nil, fmt.Errorf("unmarshaling ACK: %w", err)
	}
	if ack.MessageId != msgID {
		return nil, fmt.Errorf("ACK message ID mismatch: got %q, want %q", ack.MessageId, msgID)
	}

	now := time.Now()
	msg := &Message{
		ID:        msgID,
		From:      svc.Address,
		To:        peerAddr,
		Text:      text,
		SentAt:    time.UnixMilli(env.Timestamp),
		AckedAt:   &now,
		Direction: Outgoing,
	}

	if s.history != nil {
		s.history.Append(peerAddr, msg)
	}
	return msg, nil
}

// handleIncoming processes an incoming chat stream. The owning service is
// resolved through the registry (services.Registry.Get), exercising the
// per-service handler dispatch path; in v0 the only chat-capable service is
// the built-in "node" service registered by the daemon.
func (s *Service) handleIncoming(stream network.Stream) {
	defer stream.Close()

	stream.SetReadDeadline(time.Now().Add(transport.StreamTimeout))
	env, err := protoc.ReadMsg(stream)
	if err != nil {
		return
	}

	if env.Type != pb.MessageType_CHAT_TEXT {
		return
	}
	if !protoc.VerifyEnvelope(env) {
		return
	}

	svc, err := s.resolveService()
	if err != nil {
		return
	}
	if _, ok := svc.AsChat(); !ok {
		return
	}

	chatText := &pb.ChatText{}
	if err := proto.Unmarshal(env.Payload, chatText); err != nil {
		return
	}

	fromAddr := identity.DeriveAddress(env.PubKey).String()
	now := time.Now()
	msg := &Message{
		ID:        chatText.Id,
		From:      fromAddr,
		To:        svc.Address,
		Text:      chatText.Text,
		SentAt:    time.UnixMilli(env.Timestamp),
		AckedAt:   &now,
		Direction: Incoming,
	}

	s.ingest(msg)

	ack := &pb.ChatAck{MessageId: chatText.Id}
	ackPayload, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	ackEnv := protoc.WrapMessage(pb.MessageType_CHAT_ACK, ackPayload, svc.Identity)

	stream.SetWriteDeadline(time.Now().Add(transport.StreamTimeout))
	protoc.WriteMsg(stream, ackEnv)
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
