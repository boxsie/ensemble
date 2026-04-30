package api

import (
	"bytes"
	"context"
	"net"
	"testing"

	apipb "github.com/boxsie/ensemble/api/pb"
	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// nodeHarness wires a real node + gRPC api.Server in-memory so we can assert
// that node-backed RPCs (contacts, identity, etc.) flow request fields
// through to the underlying store. The full daemon lifecycle (Tor, libp2p,
// signaling) is not started — the tests here only exercise the api package.
type nodeHarness struct {
	t        *testing.T
	node     *node.Node
	contacts *contacts.Store
	srv      *Server
	grpc     *grpc.Server
	conn     *grpc.ClientConn
	client   apipb.EnsembleServiceClient
}

func newNodeHarness(t *testing.T) *nodeHarness {
	t.Helper()

	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	n := node.New(kp)
	cs := contacts.NewStore(t.TempDir())
	n.SetContacts(cs)

	srv := NewServer(n)
	gs := grpc.NewServer()
	apipb.RegisterEnsembleServiceServer(gs, srv)

	lis := bufconn.Listen(1 << 16)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(context.Background())
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	h := &nodeHarness{
		t:        t,
		node:     n,
		contacts: cs,
		srv:      srv,
		grpc:     gs,
		conn:     conn,
		client:   apipb.NewEnsembleServiceClient(conn),
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	})
	return h
}

// TestAddContact_PersistsPublicKey is a regression test for a bug where
// Server.AddContact built a contacts.Contact from req.Address and req.Alias
// only, dropping req.PublicKey on the floor. The signaling layer needs the
// peer's public key to verify the 3-step handshake's CONN_CONFIRM, so a
// public-key-less contact made connections silently fail with EOF on the
// initiator side. This test asserts the proto field reaches the store.
func TestAddContact_PersistsPublicKey(t *testing.T) {
	h := newNodeHarness(t)

	peerKp, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	peerAddr := identity.DeriveAddress(peerKp.PublicKey()).String()
	peerPub := peerKp.PublicKey()

	if _, err := h.client.AddContact(context.Background(), &apipb.AddContactRequest{
		Address:   peerAddr,
		Alias:     "peer",
		PublicKey: peerPub,
	}); err != nil {
		t.Fatalf("AddContact: %v", err)
	}

	got := h.contacts.Get(peerAddr)
	if got == nil {
		t.Fatal("contact not added")
	}
	if got.Address != peerAddr {
		t.Fatalf("Address: got %q, want %q", got.Address, peerAddr)
	}
	if got.Alias != "peer" {
		t.Fatalf("Alias: got %q, want %q", got.Alias, "peer")
	}
	if !bytes.Equal(got.PublicKey, peerPub) {
		t.Fatalf("PublicKey not persisted: got %x, want %x", got.PublicKey, peerPub)
	}
}

// TestAddContact_WithoutPublicKey confirms a bare contact (just address +
// alias, no key) is still accepted — the proto field is optional today.
// Pairs with TestAddContact_PersistsPublicKey to lock in both behaviors.
func TestAddContact_WithoutPublicKey(t *testing.T) {
	h := newNodeHarness(t)

	if _, err := h.client.AddContact(context.Background(), &apipb.AddContactRequest{
		Address: "Eabc123",
		Alias:   "anon",
	}); err != nil {
		t.Fatalf("AddContact: %v", err)
	}

	got := h.contacts.Get("Eabc123")
	if got == nil {
		t.Fatal("contact not added")
	}
	if len(got.PublicKey) != 0 {
		t.Fatalf("expected empty PublicKey, got %x", got.PublicKey)
	}
}
