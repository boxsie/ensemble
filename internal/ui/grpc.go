package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	apipb "github.com/boxsie/ensemble/api/pb"
	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/node"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Compile-time interface assertion.
var _ Backend = (*GRPCBackend)(nil)

// GRPCBackend wraps a gRPC client for attach mode (TUI → remote daemon).
type GRPCBackend struct {
	conn   *grpc.ClientConn
	client apipb.EnsembleServiceClient
}

// NewGRPCBackend dials the given target and returns a gRPC-backed Backend.
// For a Unix socket, use "unix:///path/to/socket".
func NewGRPCBackend(target string) (*GRPCBackend, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", target, err)
	}
	return &GRPCBackend{
		conn:   conn,
		client: apipb.NewEnsembleServiceClient(conn),
	}, nil
}

func (b *GRPCBackend) GetIdentity(ctx context.Context) (*IdentityInfo, error) {
	resp, err := b.client.GetIdentity(ctx, &apipb.GetIdentityRequest{})
	if err != nil {
		return nil, fmt.Errorf("getting identity: %w", err)
	}
	return &IdentityInfo{
		Address:   resp.Address,
		PublicKey: resp.PublicKey,
	}, nil
}

func (b *GRPCBackend) GetStatus(ctx context.Context) (*StatusInfo, error) {
	resp, err := b.client.GetStatus(ctx, &apipb.GetStatusRequest{})
	if err != nil {
		return nil, fmt.Errorf("getting status: %w", err)
	}
	return &StatusInfo{
		TorState:  resp.TorState,
		PeerCount: resp.PeerCount,
		OnionAddr: resp.OnionAddr,
		UptimeMs:  resp.UptimeMs,
	}, nil
}

func (b *GRPCBackend) ListContacts(ctx context.Context) ([]*contacts.Contact, error) {
	resp, err := b.client.ListContacts(ctx, &apipb.ListContactsRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing contacts: %w", err)
	}
	out := make([]*contacts.Contact, len(resp.Contacts))
	for i, c := range resp.Contacts {
		out[i] = contactFromProto(c)
	}
	return out, nil
}

func (b *GRPCBackend) AddContact(ctx context.Context, address, alias string, pubKey []byte) error {
	_, err := b.client.AddContact(ctx, &apipb.AddContactRequest{
		Address:   address,
		Alias:     alias,
		PublicKey: pubKey,
	})
	if err != nil {
		return fmt.Errorf("adding contact: %w", err)
	}
	return nil
}

func (b *GRPCBackend) RemoveContact(ctx context.Context, address string) error {
	_, err := b.client.RemoveContact(ctx, &apipb.RemoveContactRequest{
		Address: address,
	})
	if err != nil {
		return fmt.Errorf("removing contact: %w", err)
	}
	return nil
}

func (b *GRPCBackend) Connect(ctx context.Context, address string) (bool, string, error) {
	resp, err := b.client.Connect(ctx, &apipb.ConnectRequest{
		Address: address,
	})
	if err != nil {
		return false, "", fmt.Errorf("connecting: %w", err)
	}
	return resp.Accepted, resp.Message, nil
}

func (b *GRPCBackend) AcceptConnection(ctx context.Context, address string) error {
	_, err := b.client.AcceptConnection(ctx, &apipb.AcceptConnectionRequest{
		Address: address,
	})
	if err != nil {
		return fmt.Errorf("accepting connection: %w", err)
	}
	return nil
}

func (b *GRPCBackend) RejectConnection(ctx context.Context, address string) error {
	_, err := b.client.RejectConnection(ctx, &apipb.RejectConnectionRequest{
		Address: address,
	})
	if err != nil {
		return fmt.Errorf("rejecting connection: %w", err)
	}
	return nil
}

func (b *GRPCBackend) SendMessage(ctx context.Context, address, text string) (string, error) {
	resp, err := b.client.SendMessage(ctx, &apipb.SendMessageRequest{
		Address: address,
		Text:    text,
	})
	if err != nil {
		return "", fmt.Errorf("sending message: %w", err)
	}
	return resp.MessageId, nil
}

func (b *GRPCBackend) SendFile(ctx context.Context, address, filePath string) (<-chan FileProgress, error) {
	stream, err := b.client.SendFile(ctx, &apipb.SendFileRequest{
		Address:  address,
		FilePath: filePath,
	})
	if err != nil {
		return nil, fmt.Errorf("sending file: %w", err)
	}

	ch := make(chan FileProgress, 16)
	go func() {
		defer close(ch)
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				ch <- FileProgress{Error: err.Error()}
				return
			}
			ch <- FileProgress{
				TransferID: msg.TransferId,
				Filename:   msg.Filename,
				TotalBytes: msg.TotalBytes,
				SentBytes:  msg.SentBytes,
				Percent:    msg.Percent,
				Complete:   msg.Complete,
				Error:      msg.Error,
			}
		}
	}()

	return ch, nil
}

func (b *GRPCBackend) AcceptFile(ctx context.Context, transferID, savePath string) error {
	_, err := b.client.AcceptFile(ctx, &apipb.AcceptFileRequest{
		TransferId: transferID,
		SavePath:   savePath,
	})
	if err != nil {
		return fmt.Errorf("accepting file: %w", err)
	}
	return nil
}

func (b *GRPCBackend) RejectFile(ctx context.Context, transferID string) error {
	_, err := b.client.RejectFile(ctx, &apipb.RejectFileRequest{
		TransferId: transferID,
	})
	if err != nil {
		return fmt.Errorf("rejecting file: %w", err)
	}
	return nil
}

// eventNameTypes maps gRPC event type names back to internal EventType.
var eventNameTypes = map[string]node.EventType{
	"tor_bootstrap":      node.EventTorBootstrap,
	"tor_ready":          node.EventTorReady,
	"peer_discovered":    node.EventPeerDiscovered,
	"connection_request": node.EventConnectionReq,
	"connection_state":   node.EventConnectionState,
	"chat_message":       node.EventChatMessage,
	"file_offer":         node.EventFileOffer,
	"file_progress":      node.EventFileProgress,
	"error":              node.EventError,
}

func (b *GRPCBackend) AddNode(ctx context.Context, onionAddr string) error {
	_, err := b.client.AddNode(ctx, &apipb.AddNodeRequest{OnionAddress: onionAddr})
	if err != nil {
		return fmt.Errorf("adding node: %w", err)
	}
	return nil
}

func (b *GRPCBackend) Subscribe(ctx context.Context) (<-chan node.Event, error) {
	stream, err := b.client.Subscribe(ctx, &apipb.SubscribeRequest{})
	if err != nil {
		return nil, fmt.Errorf("subscribing: %w", err)
	}

	ch := make(chan node.Event, 64)
	go func() {
		defer close(ch)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			et, ok := eventNameTypes[msg.Type]
			if !ok {
				continue
			}
			var payload any
			if msg.Payload != "" {
				_ = json.Unmarshal([]byte(msg.Payload), &payload)
			}
			select {
			case ch <- node.Event{Type: et, Payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func (b *GRPCBackend) Close() error {
	return b.conn.Close()
}

func contactFromProto(c *apipb.ContactInfo) *contacts.Contact {
	ct := &contacts.Contact{
		Address:   c.Address,
		Alias:     c.Alias,
		PublicKey: c.PublicKey,
		OnionAddr: c.OnionAddress,
	}
	if c.AddedAt > 0 {
		ct.AddedAt = time.UnixMilli(c.AddedAt)
	}
	if c.LastSeen > 0 {
		ct.LastSeen = time.UnixMilli(c.LastSeen)
	}
	return ct
}