package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/ensemble/internal/daemon"
	"github.com/boxsie/ensemble/internal/ui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		runAttach(os.Args[2:])
		return
	}
	runDaemon(os.Args[1:])
}

// runDaemon starts the daemon and optionally the TUI.
func runDaemon(args []string) {
	fs := flag.NewFlagSet("ensemble", flag.ExitOnError)
	headless := fs.Bool("headless", false, "run daemon without TUI")
	dataDir := fs.String("data-dir", "", "override data directory")
	apiAddr := fs.String("api-addr", "", "TCP listen address for gRPC (headless mode)")
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

	backend, err := ui.NewGRPCBackend(target)
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
