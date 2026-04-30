package tor

import (
	"crypto"
	"fmt"
	"os"
	"path/filepath"

	bineed25519 "github.com/cretz/bine/torutil/ed25519"
)

// OnionServicePort is the TCP port every Ensemble onion service binds inside
// Tor. The daemon multiplexes per-service routing on the application layer;
// the transport-level port is shared.
const OnionServicePort = 9735

// loadOnionKey reads a bine ed25519 keypair from disk.
// The file format is: 64-byte private key + 32-byte public key.
// Returns os.ErrNotExist (wrapped) if the file is absent so callers can
// branch on errors.Is(err, os.ErrNotExist).
func loadOnionKey(path string) (crypto.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 96 {
		return nil, fmt.Errorf("onion key file %s has wrong size: %d (want 96)", path, len(data))
	}
	return bineed25519.PrivateKey(data[:64]).KeyPair(), nil
}

// saveOnionKey writes a bine ed25519 keypair to disk in the
// 64-byte private + 32-byte public layout used by loadOnionKey.
func saveOnionKey(path string, key crypto.PrivateKey) error {
	kp, ok := key.(bineed25519.KeyPair)
	if !ok {
		return fmt.Errorf("unsupported onion key type: %T", key)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}

	data := make([]byte, 96)
	copy(data[:64], kp.PrivateKey())
	copy(data[64:], kp.PublicKey())
	return os.WriteFile(path, data, 0600)
}
