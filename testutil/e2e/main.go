// Package main is a runnable end-to-end test that spins up two local
// ensemble daemons in separate temp data dirs, has them discover each other
// over their own Tor onion services, and verifies a chat message flows
// from one to the other.
//
// Run from the repo root:
//
//	go run ./testutil/e2e
//
// Flags:
//
//	--tor-path  override the tor binary (defaults to /usr/bin/tor)
//	--keep      do not delete the data dirs on exit (debugging)
//	--verbose   stream both daemons' stdout/stderr to this process
//
// Exits 0 on success and non-zero on any failure. Useful as a smoke test
// after changes to the signaling, transport, or chat layers — the in-tree
// unit tests stub Tor, so this is the only place the full real-Tor stack
// exercises peer-to-peer chat.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boxsie/ensemble/internal/node"
	"github.com/boxsie/ensemble/internal/ui"
)

func main() {
	torPath := flag.String("tor-path", defaultTorPath(), "path to tor binary")
	keep := flag.Bool("keep", false, "keep data dirs on exit")
	verbose := flag.Bool("verbose", false, "stream daemon logs to stdout")
	binary := flag.String("binary", "", "path to ensemble binary (default: build from source)")
	flag.Parse()

	if err := run(*torPath, *binary, *keep, *verbose); err != nil {
		log.Fatalf("e2e: %v", err)
	}
	fmt.Println("e2e: PASS")
}

func defaultTorPath() string {
	for _, p := range []string{"/usr/bin/tor", "/usr/local/bin/tor"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func run(torPath, binPath string, keep, verbose bool) error {
	if torPath == "" {
		return fmt.Errorf("no tor binary found — pass --tor-path")
	}

	if binPath == "" {
		built, err := buildEnsemble()
		if err != nil {
			return fmt.Errorf("build: %w", err)
		}
		defer os.Remove(built)
		binPath = built
		log.Printf("built %s", binPath)
	}

	// Distinct data dirs per daemon.
	dirA, err := os.MkdirTemp("", "ensemble-e2e-A-")
	if err != nil {
		return err
	}
	dirB, err := os.MkdirTemp("", "ensemble-e2e-B-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(dirA)
		defer os.RemoveAll(dirB)
	} else {
		log.Printf("--keep: data dirs %s and %s left in place", dirA, dirB)
	}

	dA, err := startDaemon(daemonOpts{
		Name:    "A",
		Binary:  binPath,
		DataDir: dirA,
		TorPath: torPath,
		Verbose: verbose,
	})
	if err != nil {
		return fmt.Errorf("start A: %w", err)
	}
	defer dA.Close()

	dB, err := startDaemon(daemonOpts{
		Name:    "B",
		Binary:  binPath,
		DataDir: dirB,
		TorPath: torPath,
		Verbose: verbose,
	})
	if err != nil {
		return fmt.Errorf("start B: %w", err)
	}
	defer dB.Close()

	log.Printf("waiting for both daemons to publish their onion service (~10-30s each)…")
	wg := sync.WaitGroup{}
	wg.Add(2)
	errs := make(chan error, 2)
	for _, d := range []*daemon{dA, dB} {
		go func() {
			defer wg.Done()
			if err := d.WaitReady(120 * time.Second); err != nil {
				errs <- fmt.Errorf("%s: %w", d.opts.Name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}

	// gRPC clients (no admin key — daemons launched without --admin-key).
	clientA, err := ui.NewGRPCBackend(dA.GRPCTarget(), nil)
	if err != nil {
		return fmt.Errorf("dial A: %w", err)
	}
	defer clientA.Close()
	clientB, err := ui.NewGRPCBackend(dB.GRPCTarget(), nil)
	if err != nil {
		return fmt.Errorf("dial B: %w", err)
	}
	defer clientB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	idA, err := clientA.GetIdentity(ctx)
	if err != nil {
		return fmt.Errorf("A.GetIdentity: %w", err)
	}
	stA, err := clientA.GetStatus(ctx)
	if err != nil {
		return fmt.Errorf("A.GetStatus: %w", err)
	}
	idB, err := clientB.GetIdentity(ctx)
	if err != nil {
		return fmt.Errorf("B.GetIdentity: %w", err)
	}
	stB, err := clientB.GetStatus(ctx)
	if err != nil {
		return fmt.Errorf("B.GetStatus: %w", err)
	}
	log.Printf("A: addr=%s onion=%s", idA.Address, stA.OnionAddr)
	log.Printf("B: addr=%s onion=%s", idB.Address, stB.OnionAddr)

	// Mutual contact (both sides need each other's pubkey for the 3-step
	// handshake to validate).
	log.Printf("A.AddContact(B); B.AddContact(A)")
	if err := clientA.AddContact(ctx, idB.Address, "B", idB.PublicKey); err != nil {
		return fmt.Errorf("A.AddContact: %w", err)
	}
	if err := clientB.AddContact(ctx, idA.Address, "A", idA.PublicKey); err != nil {
		return fmt.Errorf("B.AddContact: %w", err)
	}

	// Bootstrap A's DHT from B's onion (and vice versa) so each daemon
	// learns the (addr -> onion) mapping for the other. Run both in
	// parallel — AddNode also runs Announce internally, which dials all
	// known peers over Tor and can take ~minutes per peer on cold circuits.
	log.Printf("AddNode (both directions, in parallel)")
	go func() {
		bctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := clientA.AddNode(bctx, stB.OnionAddr); err != nil {
			log.Printf("A.AddNode warning: %v", err)
		}
	}()
	go func() {
		bctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := clientB.AddNode(bctx, stA.OnionAddr); err != nil {
			log.Printf("B.AddNode warning: %v", err)
		}
	}()

	// Don't wait for Announce — just for A's routing table to learn B,
	// which happens during the Bootstrap stage of AddNode (well before
	// Announce returns).
	if err := waitForRTContains(ctx, clientA, idB.Address, 60*time.Second); err != nil {
		return fmt.Errorf("A.RT never learned B: %w", err)
	}
	log.Printf("A.RT now contains B; proceeding to Connect")

	// Background subscription on B before sending — capture the inbound
	// chat_message event so we can assert on the text.
	log.Printf("B.Subscribe (background)")
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	bEvents, err := clientB.Subscribe(subCtx)
	if err != nil {
		return fmt.Errorf("B.Subscribe: %w", err)
	}

	// Test message — include a token so we can assert on the round trip.
	want := fmt.Sprintf("e2e %d", time.Now().UnixNano())
	gotMsg := make(chan string, 1)
	go func() {
		for ev := range bEvents {
			if ev.Type != node.EventChatMessage {
				continue
			}
			m, ok := ev.Payload.(map[string]any)
			if !ok {
				continue
			}
			text, _ := m["Text"].(string)
			if text == want {
				gotMsg <- text
				return
			}
		}
	}()

	log.Printf("A.Connect(B)")
	connCtx, connCancel := context.WithTimeout(ctx, 3*time.Minute)
	auto, msg, err := clientA.Connect(connCtx, idB.Address)
	connCancel()
	if err != nil {
		return fmt.Errorf("A.Connect: %w", err)
	}
	log.Printf("Connect: auto=%v msg=%s", auto, msg)
	if !auto {
		return fmt.Errorf("Connect did not complete: %s", msg)
	}

	log.Printf("A.SendMessage -> B: %q", want)
	if _, err := clientA.SendMessage(ctx, idB.Address, want); err != nil {
		return fmt.Errorf("A.SendMessage: %w", err)
	}

	select {
	case got := <-gotMsg:
		log.Printf("B received: %q", got)
	case <-time.After(20 * time.Second):
		return fmt.Errorf("B did not receive message within 20s")
	}

	return nil
}

// waitForRTContains polls GetDebugInfo until the daemon's routing table
// contains an entry for peerAddr, or the deadline is hit.
func waitForRTContains(ctx context.Context, client *ui.GRPCBackend, peerAddr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		di, err := client.GetDebugInfo(ctx)
		if err == nil {
			for _, p := range di.RTPeers {
				if p.Address == peerAddr {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timeout after %s", timeout)
}

// --- daemon process management ---

type daemonOpts struct {
	Name    string
	Binary  string
	DataDir string
	TorPath string
	Verbose bool
}

type daemon struct {
	opts    daemonOpts
	cmd     *exec.Cmd
	logPath string
	logFile *os.File
}

func startDaemon(o daemonOpts) (*daemon, error) {
	logPath := filepath.Join(o.DataDir, "daemon.log")
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(o.Binary,
		"--headless",
		"--data-dir", o.DataDir,
		"--tor-path", o.TorPath,
	)
	if o.Verbose {
		cmd.Stdout = io.MultiWriter(lf, prefixedWriter{prefix: "[" + o.Name + "] ", w: os.Stderr})
		cmd.Stderr = cmd.Stdout
	} else {
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, err
	}
	log.Printf("daemon %s pid=%d data=%s", o.Name, cmd.Process.Pid, o.DataDir)
	return &daemon{opts: o, cmd: cmd, logPath: logPath, logFile: lf}, nil
}

func (d *daemon) GRPCTarget() string {
	return "unix://" + filepath.Join(d.opts.DataDir, "ensemble.sock")
}

// WaitReady blocks until the log shows the per-service signaling demux is
// up — that's the moment after Tor publishes the onion AND the daemon has
// wired the listener. Anything earlier and Connect/Subscribe race.
func (d *daemon) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(d.logPath)
		if err == nil {
			s := string(b)
			if strings.Contains(s, "signaling: per-service demux started") {
				return nil
			}
			if strings.Contains(s, "tor: failed to start") {
				return fmt.Errorf("tor failed; tail of %s:\n%s", d.logPath, tail(s, 20))
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	b, _ := os.ReadFile(d.logPath)
	return fmt.Errorf("not ready in %s; tail of %s:\n%s", timeout, d.logPath, tail(string(b), 20))
}

func (d *daemon) Close() {
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- d.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = d.cmd.Process.Kill()
			<-done
		}
	}
	if d.logFile != nil {
		_ = d.logFile.Close()
	}
}

// --- helpers ---

func buildEnsemble() (string, error) {
	out, err := os.CreateTemp("", "ensemble-e2e-*")
	if err != nil {
		return "", err
	}
	out.Close()
	cmd := exec.Command("go", "build", "-o", out.Name(), ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

type prefixedWriter struct {
	prefix string
	w      io.Writer
}

func (p prefixedWriter) Write(b []byte) (int, error) {
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" {
			continue
		}
		_, _ = p.w.Write([]byte(p.prefix + line))
	}
	return len(b), nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
