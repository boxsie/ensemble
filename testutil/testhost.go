package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/transport"
)

// TestHost wraps a transport.Host configured for testing (random port, no NAT mapping).
type TestHost struct {
	Host    *transport.Host
	Keypair *identity.Keypair
	t       testing.TB
}

// NewTestHost creates a libp2p host for testing with the given keypair.
// Automatically cleaned up when the test finishes.
func NewTestHost(t testing.TB, kp *identity.Keypair) *TestHost {
	t.Helper()
	h, err := transport.NewHost(transport.HostConfig{
		Keypair:          kp,
		ListenPort:       0,
		DisableNATMap:    true,
		DisableHolePunch: true,
	})
	if err != nil {
		t.Fatalf("testutil: creating host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return &TestHost{Host: h, Keypair: kp, t: t}
}

// ConnectTo connects this host to another test host.
func (th *TestHost) ConnectTo(other *TestHost) {
	th.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := th.Host.Connect(ctx, other.Host.AddrInfo()); err != nil {
		th.t.Fatalf("testutil: connecting hosts: %v", err)
	}
}

// NewConnectedPair creates two test hosts that are already connected to each other.
func NewConnectedPair(t testing.TB) (*TestHost, *TestHost) {
	t.Helper()
	a := NewTestHost(t, Alice())
	b := NewTestHost(t, Bob())
	a.ConnectTo(b)
	return a, b
}

// MustGenerate generates a new random keypair or fails the test.
func MustGenerate(t testing.TB) *identity.Keypair {
	t.Helper()
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("testutil: generating keypair: %v", err)
	}
	return kp
}

// TempDataDir creates a temporary directory for test data.
// Automatically cleaned up when the test finishes.
func TempDataDir(t testing.TB) string {
	t.Helper()
	return t.TempDir()
}

// ContextWithTimeout returns a context with the given timeout, cancelling on test cleanup.
func ContextWithTimeout(t testing.TB, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// WaitFor repeatedly calls check until it returns true or the timeout elapses.
func WaitFor(t testing.TB, timeout time.Duration, check func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("testutil: timed out waiting for: %s", msg)
}

// Addr returns the ensemble address for a keypair.
func Addr(kp *identity.Keypair) string {
	return string(identity.DeriveAddress(kp.PublicKey()))
}

// ShortAddr returns first 8 chars of an address (for display).
func ShortAddr(addr string) string {
	if len(addr) <= 8 {
		return addr
	}
	return fmt.Sprintf("%s...", addr[:8])
}
