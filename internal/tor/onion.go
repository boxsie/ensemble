package tor

import (
	"context"
	"crypto"
	"fmt"
	"net"
	"os"
	"path/filepath"

	bineed25519 "github.com/cretz/bine/torutil/ed25519"
	binetor "github.com/cretz/bine/tor"
)

// OnionService wraps a Tor hidden service with key persistence.
type OnionService struct {
	svc *binetor.OnionService
}

// CreateOnionService creates a persistent hidden service on the given port.
// The hidden service key is stored at keyPath so the .onion address survives restarts.
// Blocks until the service is published in the Tor network.
func (e *Engine) CreateOnionService(ctx context.Context, port int, keyPath string) (*OnionService, error) {
	if e.tor == nil {
		return nil, fmt.Errorf("tor engine not started")
	}

	key, err := loadOnionKey(keyPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("loading onion key: %w", err)
	}

	svc, err := e.tor.Listen(ctx, &binetor.ListenConf{
		RemotePorts: []int{port},
		Key:         key,
		Version3:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating onion service: %w", err)
	}

	// Save newly generated key for persistence.
	if key == nil && svc.Key != nil {
		if err := saveOnionKey(keyPath, svc.Key); err != nil {
			svc.Close()
			return nil, fmt.Errorf("saving onion key: %w", err)
		}
	}

	return &OnionService{svc: svc}, nil
}

// OnionID returns the service ID (without the .onion suffix).
func (s *OnionService) OnionID() string {
	return s.svc.ID
}

// OnionAddr returns the full .onion address.
func (s *OnionService) OnionAddr() string {
	return s.svc.ID + ".onion"
}

// Listener returns the net.Listener for accepting inbound connections.
func (s *OnionService) Listener() net.Listener {
	return s.svc
}

// Close shuts down the hidden service.
func (s *OnionService) Close() error {
	return s.svc.Close()
}

// loadOnionKey reads a bine ed25519 keypair from disk.
// The file format is: 64-byte private key + 32-byte public key.
func loadOnionKey(path string) (crypto.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 96 {
		return nil, fmt.Errorf("onion key file has wrong size: %d (want 96)", len(data))
	}
	return bineed25519.PrivateKey(data[:64]).KeyPair(), nil
}

// saveOnionKey writes a bine ed25519 keypair to disk.
func saveOnionKey(path string, key crypto.PrivateKey) error {
	kp, ok := key.(bineed25519.KeyPair)
	if !ok {
		return fmt.Errorf("unsupported onion key type: %T", key)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}

	// Write 64-byte private key + 32-byte public key.
	data := make([]byte, 96)
	copy(data[:64], kp.PrivateKey())
	copy(data[64:], kp.PublicKey())
	return os.WriteFile(path, data, 0600)
}
