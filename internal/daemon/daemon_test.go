package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	apipb "github.com/boxsie/ensemble/api/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestStartAndStop(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DataDir:    dir,
		SocketPath: filepath.Join(dir, "test.sock"),
		DisableTor: true,
		DisableP2P: true,
	}

	d := New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer d.Stop()

	// Socket file should exist.
	if _, err := os.Stat(cfg.SocketPath); err != nil {
		t.Fatalf("socket file should exist: %v", err)
	}

	// Identity key file should exist under the per-service layout.
	if _, err := os.Stat(filepath.Join(dir, "services", "node", "identity.key")); err != nil {
		t.Fatalf("identity key file should exist: %v", err)
	}
}

func TestGetIdentityViaGRPC(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DataDir:    dir,
		SocketPath: filepath.Join(dir, "test.sock"),
		DisableTor: true,
		DisableP2P: true,
	}

	d := New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer d.Stop()

	// Connect via gRPC.
	conn, err := grpc.NewClient(
		"unix://"+cfg.SocketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error: %v", err)
	}
	defer conn.Close()

	client := apipb.NewEnsembleServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.GetIdentity(ctx, &apipb.GetIdentityRequest{})
	if err != nil {
		t.Fatalf("GetIdentity() error: %v", err)
	}

	if resp.Address == "" {
		t.Fatal("address should not be empty")
	}
	if len(resp.PublicKey) != 32 {
		t.Fatalf("public key size: got %d, want 32", len(resp.PublicKey))
	}

	// Address should start with E.
	if resp.Address[0] != 'E' {
		t.Fatalf("address should start with E, got %q", resp.Address)
	}
}

func TestPersistsIdentity(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DataDir:    dir,
		SocketPath: filepath.Join(dir, "test.sock"),
		DisableTor: true,
		DisableP2P: true,
	}

	// First start — generates identity.
	d1 := New(cfg)
	if err := d1.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	addr1 := d1.Node().Address().String()
	d1.Stop()

	// Second start — loads same identity.
	d2 := New(cfg)
	if err := d2.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	addr2 := d2.Node().Address().String()
	d2.Stop()

	if addr1 != addr2 {
		t.Fatalf("identity should persist: got %q then %q", addr1, addr2)
	}
}

func TestSocketCleanedUpOnStop(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DataDir:    dir,
		SocketPath: filepath.Join(dir, "test.sock"),
		DisableTor: true,
		DisableP2P: true,
	}

	d := New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	d.Stop()

	if _, err := os.Stat(cfg.SocketPath); !os.IsNotExist(err) {
		t.Fatal("socket file should be removed after Stop()")
	}
}

// TestTCPAndSocketTogether verifies the daemon can serve gRPC on both a TCP
// listener and a Unix socket simultaneously — required by the multi-service
// pod shape (TCP for ingress, socket for in-pod sidecars).
func TestTCPAndSocketTogether(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DataDir:    dir,
		SocketPath: filepath.Join(dir, "test.sock"),
		TCPAddr:    "127.0.0.1:0",
		DisableTor: true,
		DisableP2P: true,
	}

	d := New(cfg)
	if err := d.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer d.Stop()

	// Both listeners should be open.
	if _, err := os.Stat(cfg.SocketPath); err != nil {
		t.Fatalf("socket file should exist: %v", err)
	}
	if got := len(d.listeners); got != 2 {
		t.Fatalf("expected 2 listeners (TCP + socket), got %d", got)
	}

	// gRPC works over the socket.
	conn, err := grpc.NewClient(
		"unix://"+cfg.SocketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := apipb.NewEnsembleServiceClient(conn)
	if _, err := client.GetIdentity(ctx, &apipb.GetIdentityRequest{}); err != nil {
		t.Fatalf("GetIdentity over socket: %v", err)
	}

	// gRPC also works over the TCP listener — pick up the actual listening
	// address from the listener (we passed :0 to let the kernel choose).
	tcpAddr := d.listeners[0].Addr().String()
	tcpConn, err := grpc.NewClient(
		tcpAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial TCP %s: %v", tcpAddr, err)
	}
	defer tcpConn.Close()

	tcpClient := apipb.NewEnsembleServiceClient(tcpConn)
	if _, err := tcpClient.GetIdentity(ctx, &apipb.GetIdentityRequest{}); err != nil {
		t.Fatalf("GetIdentity over TCP: %v", err)
	}
}
