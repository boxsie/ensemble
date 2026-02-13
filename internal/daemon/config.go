package daemon

import (
	"os"
	"path/filepath"
)

// Config holds daemon startup configuration.
type Config struct {
	DataDir    string // base directory for identity, contacts, etc.
	Passphrase string // keystore passphrase (empty = no encryption)
	SocketPath string // Unix socket path for gRPC API
	TCPAddr    string // optional TCP address for gRPC (headless/remote)
	AdminKey   string // hex-encoded Ed25519 public key for gRPC auth
	DisableTor bool   // skip Tor startup (for testing)
	TorPath    string // explicit path to tor binary (overrides auto-download)
	DisableP2P bool   // skip libp2p host startup (for testing)
	P2PPort    int    // UDP port for QUIC (0 = random)
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".ensemble")
	return Config{
		DataDir:    dataDir,
		SocketPath: filepath.Join(dataDir, "ensemble.sock"),
	}
}
