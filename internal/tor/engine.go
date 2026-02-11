package tor

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"

	"github.com/cretz/bine/control"
	"github.com/cretz/bine/process"
	binetor "github.com/cretz/bine/tor"
)

// BootstrapEvent represents a Tor bootstrap progress update.
type BootstrapEvent struct {
	Percent int    `json:"percent"` // 0-100
	Summary string `json:"summary"` // human-readable status
}

// Engine manages an embedded Tor process.
type Engine struct {
	dataDir  string
	exePath  string // path to tor binary; empty = find on PATH
	tor      *binetor.Tor
	progress chan BootstrapEvent
	ready    chan struct{}

	mu        sync.Mutex
	socksAddr string
	stopped   bool
}

// NewEngine creates a Tor engine that stores state in dataDir/tor.
// exePath is the path to the tor binary; pass "" to find "tor" on PATH.
func NewEngine(dataDir string, exePath string) *Engine {
	return &Engine{
		dataDir:  filepath.Join(dataDir, "tor"),
		exePath:  exePath,
		progress: make(chan BootstrapEvent, 64),
		ready:    make(chan struct{}),
	}
}

// Start launches the Tor process and begins bootstrapping.
// Bootstrap progress is sent to ProgressChan(). The Ready() channel is
// closed when bootstrap reaches 100%.
func (e *Engine) Start(ctx context.Context) error {
	torPath := e.exePath
	if torPath == "" {
		var err error
		torPath, err = EnsureTor(ctx, filepath.Dir(e.dataDir))
		if err != nil {
			return fmt.Errorf("tor binary not available: %w", err)
		}
	}

	// Create an empty defaults-torrc to prevent system /etc/tor/torrc
	// from injecting options like ControlSocket that require root.
	emptyDefaults := filepath.Join(e.dataDir, "defaults-torrc")
	if err := os.MkdirAll(e.dataDir, 0700); err != nil {
		return fmt.Errorf("creating tor data dir: %w", err)
	}
	if err := os.WriteFile(emptyDefaults, nil, 0600); err != nil {
		return fmt.Errorf("creating empty defaults-torrc: %w", err)
	}

	t, err := binetor.Start(ctx, &binetor.StartConf{
		ProcessCreator: torProcessCreator(torPath),
		DataDir:        e.dataDir,
		EnableNetwork:  false,
		ExtraArgs:      []string{"--defaults-torrc", emptyDefaults},
	})
	if err != nil {
		return fmt.Errorf("starting tor: %w", err)
	}
	e.tor = t

	go e.bootstrap(ctx)
	return nil
}

// bootstrap enables the network and monitors progress events.
func (e *Engine) bootstrap(ctx context.Context) {
	events := make(chan control.Event, 64)
	if err := e.tor.Control.AddEventListener(events, control.EventCodeStatusClient); err != nil {
		log.Printf("tor: failed to add event listener: %v", err)
		return
	}

	// Start the background event reader. Without this, bine never reads
	// async events from the control connection and our channel stays empty.
	eventCtx, eventCancel := context.WithCancel(ctx)
	defer eventCancel()
	go e.tor.Control.HandleEvents(eventCtx)

	if err := e.tor.Control.SetConf(control.KeyVals("DisableNetwork", "0")...); err != nil {
		log.Printf("tor: failed to enable network: %v", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				return
			}
			status, ok := evt.(*control.StatusEvent)
			if !ok || status.Action != "BOOTSTRAP" {
				continue
			}

			pct, _ := strconv.Atoi(status.Arguments["PROGRESS"])
			be := BootstrapEvent{
				Percent: pct,
				Summary: status.Arguments["SUMMARY"],
			}

			// Non-blocking send to progress channel.
			select {
			case e.progress <- be:
			default:
			}

			if pct >= 100 {
				e.fetchSOCKSAddr()
				close(e.ready)
				return
			}

			if status.Severity == "ERR" {
				log.Printf("tor: bootstrap error: %s", status.Arguments["WARNING"])
				return
			}
		}
	}
}

// fetchSOCKSAddr queries Tor for the auto-assigned SOCKS port.
func (e *Engine) fetchSOCKSAddr() {
	info, err := e.tor.Control.GetInfo("net/listeners/socks")
	if err != nil || len(info) == 0 {
		log.Printf("tor: failed to get socks address: %v", err)
		return
	}

	e.mu.Lock()
	e.socksAddr = info[0].Val
	e.mu.Unlock()
}

// ProgressChan returns a channel that receives bootstrap progress events.
func (e *Engine) ProgressChan() <-chan BootstrapEvent {
	return e.progress
}

// Ready returns a channel that is closed when Tor finishes bootstrapping.
func (e *Engine) Ready() <-chan struct{} {
	return e.ready
}

// SOCKSAddr returns the SOCKS5 proxy address (e.g. "127.0.0.1:9050").
// Empty string if Tor is not yet ready.
func (e *Engine) SOCKSAddr() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.socksAddr
}

// Tor returns the underlying bine Tor instance.
// Used by onion service and dialer. Nil before Start().
func (e *Engine) Tor() *binetor.Tor {
	return e.tor
}

// torProcessCreator returns a bine process Creator that launches the tor
// binary with LD_LIBRARY_PATH (or DYLD_LIBRARY_PATH on macOS) set to the
// binary's directory so bundled shared libraries are found.
func torProcessCreator(torPath string) process.Creator {
	libDir := filepath.Dir(torPath)
	return process.CmdCreatorFunc(func(ctx context.Context, args ...string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, torPath, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		switch runtime.GOOS {
		case "darwin":
			cmd.Env = append(cmd.Env, "DYLD_LIBRARY_PATH="+libDir)
		default:
			cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH="+libDir)
		}
		return cmd, nil
	})
}

// Stop gracefully shuts down the Tor process.
func (e *Engine) Stop() error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	e.mu.Unlock()

	if e.tor != nil {
		return e.tor.Close()
	}
	return nil
}
