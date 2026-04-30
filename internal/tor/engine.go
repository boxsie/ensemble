package tor

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"

	"github.com/cretz/bine/control"
	"github.com/cretz/bine/process"
	binetor "github.com/cretz/bine/tor"
	bineed25519 "github.com/cretz/bine/torutil/ed25519"
)

// BootstrapEvent represents a Tor bootstrap progress update.
type BootstrapEvent struct {
	Percent int    `json:"percent"` // 0-100
	Summary string `json:"summary"` // human-readable status
}

// Keypair is the bine ed25519 onion-service keypair type. Its Save/Load form
// is 64-byte private || 32-byte public on disk; in memory it is the
// precomputed key form bine's tor.Listen accepts.
type Keypair = bineed25519.KeyPair

// ErrOnionNotFound is returned when an onion is not registered with the engine.
var ErrOnionNotFound = errors.New("tor: onion service not found")

// ErrOnionExists is returned when AddOnion is called with a name that is
// already in use.
var ErrOnionExists = errors.New("tor: onion service already exists")

// onionService bundles a running bine hidden service with the on-disk key path
// it persists to.
type onionService struct {
	svc     *binetor.OnionService
	keyPath string
}

// Engine manages an embedded Tor process and any number of onion services
// hosted by it. AddOnion / RemoveOnion are safe to call concurrently and do
// not restart the underlying Tor process.
type Engine struct {
	dataDir  string
	exePath  string // path to tor binary; empty = find on PATH
	tor      *binetor.Tor
	progress chan BootstrapEvent
	ready    chan struct{}

	mu        sync.RWMutex
	socksAddr string
	stopped   bool
	onions    map[string]*onionService
}

// NewEngine creates a Tor engine that stores state in dataDir/tor.
// Onion-service keys live alongside identity keys at
// <dataDir>/services/<name>/onion.key.
// exePath is the path to the tor binary; pass "" to find "tor" on PATH.
func NewEngine(dataDir string, exePath string) *Engine {
	return &Engine{
		dataDir:  dataDir,
		exePath:  exePath,
		progress: make(chan BootstrapEvent, 64),
		ready:    make(chan struct{}),
		onions:   make(map[string]*onionService),
	}
}

// torDataDir returns the directory used for the Tor process's own state.
func (e *Engine) torDataDir() string {
	return filepath.Join(e.dataDir, "tor")
}

// onionKeyPath returns the on-disk path for a service's onion key.
func (e *Engine) onionKeyPath(name string) string {
	return filepath.Join(e.dataDir, "services", name, "onion.key")
}

// Start launches the Tor process and begins bootstrapping.
// Bootstrap progress is sent to ProgressChan(). The Ready() channel is
// closed when bootstrap reaches 100%.
func (e *Engine) Start(ctx context.Context) error {
	torPath := e.exePath
	if torPath == "" {
		var err error
		torPath, err = EnsureTor(ctx, e.dataDir)
		if err != nil {
			return fmt.Errorf("tor binary not available: %w", err)
		}
	}

	// Create an empty defaults-torrc to prevent system /etc/tor/torrc
	// from injecting options like ControlSocket that require root.
	torDir := e.torDataDir()
	emptyDefaults := filepath.Join(torDir, "defaults-torrc")
	if err := os.MkdirAll(torDir, 0700); err != nil {
		return fmt.Errorf("creating tor data dir: %w", err)
	}
	if err := os.WriteFile(emptyDefaults, nil, 0600); err != nil {
		return fmt.Errorf("creating empty defaults-torrc: %w", err)
	}

	t, err := binetor.Start(ctx, &binetor.StartConf{
		ProcessCreator: torProcessCreator(torPath),
		DataDir:        torDir,
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
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.socksAddr
}

// Tor returns the underlying bine Tor instance.
// Used by onion service and dialer. Nil before Start().
func (e *Engine) Tor() *binetor.Tor {
	return e.tor
}

// AddOnion publishes a new hidden service under the given name on the
// shared service port. If kp is non-nil it is used as the onion keypair;
// otherwise the engine loads <dataDir>/services/<name>/onion.key from disk
// (yielding the same address across restarts) or generates a new key and
// persists it.
//
// Returns ErrOnionExists if a service with this name is already running on
// this engine.
func (e *Engine) AddOnion(ctx context.Context, name string, kp Keypair) (string, error) {
	if name == "" {
		return "", fmt.Errorf("tor: onion name must not be empty")
	}
	if e.tor == nil {
		return "", fmt.Errorf("tor: engine not started")
	}

	e.mu.Lock()
	if _, ok := e.onions[name]; ok {
		e.mu.Unlock()
		return "", ErrOnionExists
	}
	e.mu.Unlock()

	keyPath := e.onionKeyPath(name)

	var key crypto.PrivateKey
	if kp != nil {
		key = kp
	} else {
		loaded, err := loadOnionKey(keyPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("loading onion key for %q: %w", name, err)
		}
		if loaded != nil {
			key = loaded
		}
	}

	svc, err := e.tor.Listen(ctx, &binetor.ListenConf{
		RemotePorts: []int{OnionServicePort},
		Key:         key,
		Version3:    true,
	})
	if err != nil {
		return "", fmt.Errorf("publishing onion %q: %w", name, err)
	}

	if key == nil && svc.Key != nil {
		if err := saveOnionKey(keyPath, svc.Key); err != nil {
			svc.Close()
			return "", fmt.Errorf("saving onion key for %q: %w", name, err)
		}
	}

	e.mu.Lock()
	if _, ok := e.onions[name]; ok {
		e.mu.Unlock()
		svc.Close()
		return "", ErrOnionExists
	}
	e.onions[name] = &onionService{svc: svc, keyPath: keyPath}
	e.mu.Unlock()

	return svc.ID + ".onion", nil
}

// RemoveOnion tears down a running onion service. The on-disk key file is
// retained so a subsequent AddOnion under the same name re-publishes the
// same .onion address. Returns ErrOnionNotFound if no such service is running.
func (e *Engine) RemoveOnion(name string) error {
	e.mu.Lock()
	svc, ok := e.onions[name]
	if !ok {
		e.mu.Unlock()
		return ErrOnionNotFound
	}
	delete(e.onions, name)
	e.mu.Unlock()

	if err := svc.svc.Close(); err != nil {
		return fmt.Errorf("closing onion %q: %w", name, err)
	}
	return nil
}

// OnionAddr returns the .onion address registered under name, plus a bool
// indicating whether the service is currently running on this engine.
func (e *Engine) OnionAddr(name string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	svc, ok := e.onions[name]
	if !ok {
		return "", false
	}
	return svc.svc.ID + ".onion", true
}

// OnionListener returns the inbound listener for name. Returns nil and false
// if no such service is running.
func (e *Engine) OnionListener(name string) (net.Listener, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	svc, ok := e.onions[name]
	if !ok {
		return nil, false
	}
	return svc.svc, true
}

// ListOnions returns the names of every onion service currently running on
// this engine, sorted lexicographically.
func (e *Engine) ListOnions() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.onions))
	for name := range e.onions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// Stop gracefully shuts down all onion services and the Tor process.
func (e *Engine) Stop() error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	onions := e.onions
	e.onions = make(map[string]*onionService)
	e.mu.Unlock()

	for _, svc := range onions {
		_ = svc.svc.Close()
	}

	if e.tor != nil {
		return e.tor.Close()
	}
	return nil
}
