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

// Service handles sending and receiving chat messages over libp2p streams.
type Service struct {
	keypair  *identity.Keypair
	address  string
	host     *transport.Host
	eventBus *node.EventBus
	resolver PeerResolver
	history  *History
}

// NewService creates a chat service and registers the incoming stream handler.
// If history is non-nil, sent and received messages are automatically recorded.
func NewService(kp *identity.Keypair, host *transport.Host, bus *node.EventBus, resolver PeerResolver, history *History) *Service {
	s := &Service{
		keypair:  kp,
		address:  identity.DeriveAddress(kp.PublicKey()).String(),
		host:     host,
		eventBus: bus,
		resolver: resolver,
		history:  history,
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

// SendMessage sends a text message to a peer and waits for an ACK.
func (s *Service) SendMessage(ctx context.Context, peerAddr string, text string) (*Message, error) {
	pc := s.resolver.GetPeer(peerAddr)
	if pc == nil {
		return nil, fmt.Errorf("peer %s not connected", peerAddr)
	}

	return s.sendTo(ctx, pc.PeerID, peerAddr, text)
}

// sendTo sends a message to a specific libp2p peer.
func (s *Service) sendTo(ctx context.Context, pid peer.ID, peerAddr string, text string) (*Message, error) {
	msgID := generateID()

	chatText := &pb.ChatText{Id: msgID, Text: text}
	payload, err := proto.Marshal(chatText)
	if err != nil {
		return nil, fmt.Errorf("marshaling chat text: %w", err)
	}

	env := protoc.WrapMessage(pb.MessageType_CHAT_TEXT, payload, s.keypair)

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

	// Wait for ACK.
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
		From:      s.address,
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

// handleIncoming processes an incoming chat stream.
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

	chatText := &pb.ChatText{}
	if err := proto.Unmarshal(env.Payload, chatText); err != nil {
		return
	}

	fromAddr := identity.DeriveAddress(env.PubKey).String()
	now := time.Now()
	msg := &Message{
		ID:        chatText.Id,
		From:      fromAddr,
		To:        s.address,
		Text:      chatText.Text,
		SentAt:    time.UnixMilli(env.Timestamp),
		AckedAt:   &now,
		Direction: Incoming,
	}

	if s.history != nil {
		s.history.Append(fromAddr, msg)
	}

	s.eventBus.Publish(node.Event{
		Type:    node.EventChatMessage,
		Payload: msg,
	})

	// Send ACK back.
	ack := &pb.ChatAck{MessageId: chatText.Id}
	ackPayload, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	ackEnv := protoc.WrapMessage(pb.MessageType_CHAT_ACK, ackPayload, s.keypair)

	stream.SetWriteDeadline(time.Now().Add(transport.StreamTimeout))
	protoc.WriteMsg(stream, ackEnv)
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
