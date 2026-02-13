package ui

import (
	"context"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/node"
)

// IdentityInfo holds the node's address and public key.
type IdentityInfo struct {
	Address   string
	PublicKey []byte
}

// StatusInfo holds the node's current status.
type StatusInfo struct {
	TorState  string
	PeerCount int32
	OnionAddr string
	UptimeMs  int64
}

// FileProgress reports file transfer progress.
type FileProgress struct {
	TransferID string
	Filename   string
	TotalBytes uint64
	SentBytes  uint64
	Percent    float32
	Complete   bool
	Error      string
}

// Backend abstracts daemon access so the TUI works identically
// whether running in-process or attached to a remote daemon.
type Backend interface {
	GetIdentity(ctx context.Context) (*IdentityInfo, error)
	GetStatus(ctx context.Context) (*StatusInfo, error)
	ListContacts(ctx context.Context) ([]*contacts.Contact, error)
	AddContact(ctx context.Context, address, alias string, pubKey []byte) error
	RemoveContact(ctx context.Context, address string) error
	Connect(ctx context.Context, address string) (accepted bool, msg string, err error)
	AcceptConnection(ctx context.Context, address string) error
	RejectConnection(ctx context.Context, address string) error
	SendMessage(ctx context.Context, address, text string) (messageID string, err error)
	SendFile(ctx context.Context, address, filePath string) (<-chan FileProgress, error)
	AcceptFile(ctx context.Context, transferID, savePath string) error
	RejectFile(ctx context.Context, transferID string) error
	AddNode(ctx context.Context, onionAddr string) (peersFound int, err error)
	Subscribe(ctx context.Context) (<-chan node.Event, error)
	Close() error
}
