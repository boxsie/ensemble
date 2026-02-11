package ui

import (
	"context"
	"fmt"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/transport"
)

// Compile-time interface assertion.
var _ Backend = (*DirectBackend)(nil)

// directNode is the interface DirectBackend requires from the node.
type directNode interface {
	Identity() *identity.Keypair
	Address() identity.Address
	EventBus() *node.EventBus
	TorState() string
	OnionAddr() string
	Connect(ctx context.Context, peerAddr string) (*signaling.HandshakeResult, error)
	AcceptConnection(addr string) error
	RejectConnection(addr string, reason string) error
	PeerCount() int32
	Contacts() *contacts.Store
	ConnectionInfo(addr string) *transport.PeerConnection
	ActiveConnections() []*transport.PeerConnection
	SendMessage(ctx context.Context, addr, text string) (string, error)
	SendFile(ctx context.Context, peerAddr, filePath string) (string, error)
	AcceptFile(transferID, savePath string) error
	RejectFile(transferID, reason string) error
}

// DirectBackend wraps a node.Node for in-process TUI access (no serialization).
type DirectBackend struct {
	node directNode
}

// NewDirectBackend creates a backend that talks to the node directly.
func NewDirectBackend(n directNode) *DirectBackend {
	return &DirectBackend{node: n}
}

func (b *DirectBackend) GetIdentity(_ context.Context) (*IdentityInfo, error) {
	return &IdentityInfo{
		Address:   b.node.Address().String(),
		PublicKey: b.node.Identity().PublicKey(),
	}, nil
}

func (b *DirectBackend) GetStatus(_ context.Context) (*StatusInfo, error) {
	return &StatusInfo{
		TorState:  b.node.TorState(),
		OnionAddr: b.node.OnionAddr(),
		PeerCount: b.node.PeerCount(),
	}, nil
}

func (b *DirectBackend) ListContacts(_ context.Context) ([]*contacts.Contact, error) {
	cs := b.node.Contacts()
	if cs == nil {
		return nil, nil
	}
	return cs.List(), nil
}

func (b *DirectBackend) AddContact(_ context.Context, addr, alias string, pubKey []byte) error {
	cs := b.node.Contacts()
	if cs == nil {
		return fmt.Errorf("contacts not initialized")
	}
	cs.Add(&contacts.Contact{
		Address:   addr,
		Alias:     alias,
		PublicKey: pubKey,
	})
	return cs.Save()
}

func (b *DirectBackend) RemoveContact(_ context.Context, addr string) error {
	cs := b.node.Contacts()
	if cs == nil {
		return fmt.Errorf("contacts not initialized")
	}
	cs.Remove(addr)
	return cs.Save()
}

func (b *DirectBackend) Connect(ctx context.Context, addr string) (bool, string, error) {
	_, err := b.node.Connect(ctx, addr)
	if err != nil {
		return false, err.Error(), nil
	}
	return true, "connected", nil
}

func (b *DirectBackend) AcceptConnection(_ context.Context, addr string) error {
	return b.node.AcceptConnection(addr)
}

func (b *DirectBackend) RejectConnection(_ context.Context, addr string) error {
	return b.node.RejectConnection(addr, "rejected by user")
}

func (b *DirectBackend) SendMessage(ctx context.Context, addr, text string) (string, error) {
	return b.node.SendMessage(ctx, addr, text)
}

func (b *DirectBackend) SendFile(ctx context.Context, addr, filePath string) (<-chan FileProgress, error) {
	ch := make(chan FileProgress, 1)
	go func() {
		defer close(ch)
		transferID, err := b.node.SendFile(ctx, addr, filePath)
		if err != nil {
			ch <- FileProgress{Error: err.Error()}
			return
		}
		ch <- FileProgress{TransferID: transferID, Complete: true}
	}()
	return ch, nil
}

func (b *DirectBackend) AcceptFile(_ context.Context, transferID, savePath string) error {
	return b.node.AcceptFile(transferID, savePath)
}

func (b *DirectBackend) RejectFile(_ context.Context, transferID string) error {
	return b.node.RejectFile(transferID, "rejected by user")
}

func (b *DirectBackend) Subscribe(ctx context.Context) (<-chan node.Event, error) {
	return b.node.EventBus().SubscribeAll(ctx), nil
}

func (b *DirectBackend) Close() error {
	return nil // node lifecycle managed by daemon
}