package services

import (
	"context"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/tor"
)

// ACL declares who is allowed to interact with a service. Only ACLContacts is
// enforced at runtime in v0; the other tiers are stored on the Manifest for
// forward compatibility with T07's external services.
type ACL int

const (
	ACLPublic ACL = iota
	ACLContacts
	ACLAllowlist
)

// String renders the ACL tier for logs and debug output.
func (a ACL) String() string {
	switch a {
	case ACLPublic:
		return "public"
	case ACLContacts:
		return "contacts"
	case ACLAllowlist:
		return "allowlist"
	default:
		return "unknown"
	}
}

// Manifest is the declared metadata for a service. It travels alongside the
// Service record and is what the daemon shows in debug output and (eventually)
// returns from a ListServices RPC.
type Manifest struct {
	Description string
	ACL         ACL
	Allowlist   []string
}

// ConnectionRequest carries the minimum information a handler needs to decide
// whether to accept an inbound peer. The signaling layer fills in FromAddr
// (and any future fields like requested-pubkey) before invoking the handler.
type ConnectionRequest struct {
	FromAddr string
}

// ConnectionHandler is implemented by services that want to make accept/reject
// decisions on inbound peers beyond what the registry-level ACL provides.
// Returning false (with a nil error) cleanly rejects; returning an error is
// treated as a rejection and surfaced in logs.
type ConnectionHandler interface {
	OnConnectionRequest(ctx context.Context, req ConnectionRequest) (bool, error)
}

// ChatHandler is implemented by services that accept inbound chat messages.
// fromAddr is the peer's E… address as validated by the signaling layer.
type ChatHandler interface {
	OnChatMessage(ctx context.Context, fromAddr, text string) error
}

// FileOffer is the registry-level view of an inbound file offer. The full
// protocol-level type lives in internal/filetransfer; the registry refuses
// to import it to avoid a dependency cycle once filetransfer migrates onto
// the registry in T06. ID lets the handler accept/reject the specific offer.
type FileOffer struct {
	ID       string
	Filename string
	Size     int64
}

// FileHandler is implemented by services that accept inbound file offers.
type FileHandler interface {
	OnFileOffer(ctx context.Context, fromAddr string, offer FileOffer) error
}

// Service is the immutable record of a registered service. Every field is set
// at Register time and never mutated afterwards.
type Service struct {
	Name      string
	Address   string
	OnionAddr string
	Identity  *identity.Keypair
	Manifest  Manifest
	Handlers  any
}

// AsChat reports whether the service's handler set implements ChatHandler.
func (s *Service) AsChat() (ChatHandler, bool) {
	if s == nil || s.Handlers == nil {
		return nil, false
	}
	h, ok := s.Handlers.(ChatHandler)
	return h, ok
}

// AsFile reports whether the service's handler set implements FileHandler.
func (s *Service) AsFile() (FileHandler, bool) {
	if s == nil || s.Handlers == nil {
		return nil, false
	}
	h, ok := s.Handlers.(FileHandler)
	return h, ok
}

// AsConnection reports whether the service's handler set implements
// ConnectionHandler.
func (s *Service) AsConnection() (ConnectionHandler, bool) {
	if s == nil || s.Handlers == nil {
		return nil, false
	}
	h, ok := s.Handlers.(ConnectionHandler)
	return h, ok
}

// OnionEngine is the slice of *tor.Engine that the registry depends on. The
// real tor.Engine satisfies this interface; tests substitute an in-memory
// fake to avoid spinning up a Tor process per test.
type OnionEngine interface {
	AddOnion(ctx context.Context, name string, kp tor.Keypair) (string, error)
	RemoveOnion(name string) error
}
