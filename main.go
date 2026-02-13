package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/ensemble/internal/daemon"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/ui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "attach":
			runAttach(os.Args[2:])
			return
		case "debug":
			runDebug(os.Args[2:])
			return
		case "keygen":
			runKeygen(os.Args[2:])
			return
		case "add-node":
			runAddNode(os.Args[2:])
			return
		}
	}
	runDaemon(os.Args[1:])
}

// runDaemon starts the daemon and optionally the TUI.
func runDaemon(args []string) {
	fs := flag.NewFlagSet("ensemble", flag.ExitOnError)
	headless := fs.Bool("headless", false, "run daemon without TUI")
	dataDir := fs.String("data-dir", "", "override data directory")
	apiAddr := fs.String("api-addr", "", "TCP listen address for gRPC (headless mode)")
	adminKey := fs.String("admin-key", os.Getenv("ENSEMBLE_ADMIN_KEY"), "hex-encoded Ed25519 admin public key (or ENSEMBLE_ADMIN_KEY env)")
	torPath := fs.String("tor-path", "", "path to tor binary (skip auto-download)")
	fs.Parse(args)

	cfg := daemon.DefaultConfig()
	if *dataDir != "" {
		cfg.DataDir = *dataDir
		cfg.SocketPath = filepath.Join(*dataDir, "ensemble.sock")
	}
	if *apiAddr != "" {
		cfg.TCPAddr = *apiAddr
	}
	if *adminKey != "" {
		cfg.AdminKey = *adminKey
	}
	if *torPath != "" {
		cfg.TorPath = *torPath
	}

	d := daemon.New(cfg)
	if err := d.Start(); err != nil {
		log.Fatalf("failed to start daemon: %v", err)
	}
	defer d.Stop()

	if *headless {
		log.Printf("running in headless mode")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("shutting down")
		return
	}

	// In-process TUI.
	backend := ui.NewDirectBackend(d.Node())
	app := ui.NewApp(backend)
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}
}

// runAttach connects a TUI to an already-running daemon.
func runAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	socketPath := fs.String("socket", "", "daemon socket path")
	addr := fs.String("addr", "", "daemon TCP address (e.g. localhost:9090)")
	authKey := fs.String("auth-key", "", "path to Ed25519 seed file for authentication")
	fs.Parse(args)

	var target string
	switch {
	case *addr != "":
		target = *addr
	case *socketPath != "":
		target = "unix://" + *socketPath
	default:
		home, _ := os.UserHomeDir()
		target = "unix://" + filepath.Join(home, ".ensemble", "ensemble.sock")
	}

	var privKey ed25519.PrivateKey
	if *authKey != "" {
		var err error
		privKey, err = loadAuthKey(*authKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading auth key: %v\n", err)
			os.Exit(1)
		}
	}

	backend, err := ui.NewGRPCBackend(target, privKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer backend.Close()

	app := ui.NewApp(backend)
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// runDebug connects via gRPC and prints diagnostic info to stdout.
func runDebug(args []string) {
	fs := flag.NewFlagSet("debug", flag.ExitOnError)
	socketPath := fs.String("socket", "", "daemon socket path")
	addr := fs.String("addr", "", "daemon TCP address (e.g. localhost:9090)")
	authKey := fs.String("auth-key", "", "path to Ed25519 seed file for authentication")
	fs.Parse(args)

	var target string
	switch {
	case *addr != "":
		target = *addr
	case *socketPath != "":
		target = "unix://" + *socketPath
	default:
		home, _ := os.UserHomeDir()
		target = "unix://" + filepath.Join(home, ".ensemble", "ensemble.sock")
	}

	var privKey ed25519.PrivateKey
	if *authKey != "" {
		var err error
		privKey, err = loadAuthKey(*authKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading auth key: %v\n", err)
			os.Exit(1)
		}
	}

	backend, err := ui.NewGRPCBackend(target, privKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to daemon: %v\n", err)
		os.Exit(1)
	}
	defer backend.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := backend.GetDebugInfo(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetDebugInfo failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Onion: %s\n", info.OnionAddr)
	fmt.Printf("\nRouting Table (%d peers):\n", info.RTSize)
	if len(info.RTPeers) == 0 {
		fmt.Println("  (empty)")
	} else {
		for _, p := range info.RTPeers {
			ago := time.Since(time.UnixMilli(p.LastSeen)).Truncate(time.Second)
			fmt.Printf("  %-36s  %-62s  %s ago\n", p.Address, p.OnionAddr, ago)
		}
	}

	fmt.Printf("\nConnections (%d):\n", len(info.Connections))
	if len(info.Connections) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, c := range info.Connections {
			line := fmt.Sprintf("  %-36s  %s", c.Address, c.State)
			if c.Error != "" {
				line += fmt.Sprintf("  err: %s", c.Error)
			}
			fmt.Println(line)
		}
	}
}

// runKeygen generates an Ed25519 admin keypair for gRPC authentication.
func runKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	output := fs.String("output", "admin.key", "path to save the private seed file")
	fs.Parse(args)

	kp, err := identity.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generating keypair: %v\n", err)
		os.Exit(1)
	}

	// Save the 32-byte seed (enough to reconstruct the full keypair).
	if err := os.WriteFile(*output, kp.Seed(), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "writing key file: %v\n", err)
		os.Exit(1)
	}

	pubHex := hex.EncodeToString(kp.PublicKey())
	fmt.Printf("Admin key generated.\n\n")
	fmt.Printf("  Private seed: %s  (keep secret!)\n", *output)
	fmt.Printf("  Public key:   %s\n\n", pubHex)
	fmt.Printf("Server:  ensemble --headless --admin-key %s\n", pubHex)
	fmt.Printf("Client:  ensemble debug --auth-key %s --addr host:9090\n", *output)
}

// runAddNode bootstraps from a seed node's onion address.
func runAddNode(args []string) {
	fs := flag.NewFlagSet("add-node", flag.ExitOnError)
	socketPath := fs.String("socket", "", "daemon socket path")
	addr := fs.String("addr", "", "daemon TCP address")
	authKey := fs.String("auth-key", "", "path to Ed25519 seed file for authentication")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: ensemble add-node [flags] <onion-address>\n")
		os.Exit(1)
	}
	onionAddr := fs.Arg(0)

	var target string
	switch {
	case *addr != "":
		target = *addr
	case *socketPath != "":
		target = "unix://" + *socketPath
	default:
		home, _ := os.UserHomeDir()
		target = "unix://" + filepath.Join(home, ".ensemble", "ensemble.sock")
	}

	var privKey ed25519.PrivateKey
	if *authKey != "" {
		var err error
		privKey, err = loadAuthKey(*authKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loading auth key: %v\n", err)
			os.Exit(1)
		}
	}

	backend, err := ui.NewGRPCBackend(target, privKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer backend.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	n, err := backend.AddNode(ctx, onionAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AddNode failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Bootstrap complete: %d peers discovered\n", n)
}

// loadAuthKey reads a 32-byte Ed25519 seed file and returns the private key.
func loadAuthKey(path string) (ed25519.PrivateKey, error) {
	seed, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid seed file: got %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

