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

	// Identity key file should exist.
	if _, err := os.Stat(filepath.Join(dir, "identity.key")); err != nil {
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
