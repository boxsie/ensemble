//go:build integration

package tor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// integrationTorPath returns an absolute path to a tor binary. Honors the
// TOR_BIN env var, otherwise falls back to whatever's on PATH. Skips the test
// if neither is usable so integration runs degrade cleanly on CI without Tor.
func integrationTorPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("TOR_BIN"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("TOR_BIN=%q not usable: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("tor")
	if err != nil {
		t.Skipf("tor binary not available: %v", err)
	}
	return p
}

func TestEngineStartStop(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir, integrationTorPath(t))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer eng.Stop()

	select {
	case evt := <-eng.ProgressChan():
		if evt.Percent < 0 || evt.Percent > 100 {
			t.Errorf("unexpected progress percent: %d", evt.Percent)
		}
		t.Logf("progress: %d%% — %s", evt.Percent, evt.Summary)
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap progress")
	}

	select {
	case <-eng.Ready():
		t.Log("tor is ready")
	case <-ctx.Done():
		t.Fatal("timed out waiting for tor to be ready")
	}

	if addr := eng.SOCKSAddr(); addr == "" {
		t.Error("SOCKSAddr() is empty after bootstrap")
	}

	if err := eng.Stop(); err != nil {
		t.Errorf("Stop() error: %v", err)
	}
	if err := eng.Stop(); err != nil {
		t.Errorf("double Stop() error: %v", err)
	}
}

// TestMultiOnion verifies that:
//  1. Two onion services can run concurrently in one engine with distinct addresses.
//  2. Removing one does not affect the other.
//  3. Re-adding the same name yields the same .onion address (key persisted on disk).
func TestMultiOnion(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir, integrationTorPath(t))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer eng.Stop()

	select {
	case <-eng.Ready():
	case <-ctx.Done():
		t.Fatal("timed out waiting for tor to be ready")
	}

	addrA, err := eng.AddOnion(ctx, "alpha", nil)
	if err != nil {
		t.Fatalf("AddOnion(alpha): %v", err)
	}
	addrB, err := eng.AddOnion(ctx, "beta", nil)
	if err != nil {
		t.Fatalf("AddOnion(beta): %v", err)
	}
	t.Logf("alpha=%s beta=%s", addrA, addrB)

	if addrA == addrB {
		t.Fatalf("two onions share an address: %s", addrA)
	}

	if got, ok := eng.OnionAddr("alpha"); !ok || got != addrA {
		t.Errorf("OnionAddr(alpha) = (%q,%v), want (%q,true)", got, ok, addrA)
	}
	if got, ok := eng.OnionAddr("beta"); !ok || got != addrB {
		t.Errorf("OnionAddr(beta) = (%q,%v), want (%q,true)", got, ok, addrB)
	}

	names := eng.ListOnions()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("ListOnions = %v, want [alpha beta]", names)
	}

	// Adding the same name again must fail.
	if _, err := eng.AddOnion(ctx, "alpha", nil); !errors.Is(err, ErrOnionExists) {
		t.Errorf("re-AddOnion(alpha): want ErrOnionExists, got %v", err)
	}

	// Remove alpha; beta must remain reachable.
	if err := eng.RemoveOnion("alpha"); err != nil {
		t.Fatalf("RemoveOnion(alpha): %v", err)
	}
	if _, ok := eng.OnionAddr("alpha"); ok {
		t.Errorf("OnionAddr(alpha) still present after remove")
	}
	if got, ok := eng.OnionAddr("beta"); !ok || got != addrB {
		t.Errorf("OnionAddr(beta) after removing alpha = (%q,%v), want (%q,true)", got, ok, addrB)
	}
	if got := eng.ListOnions(); len(got) != 1 || got[0] != "beta" {
		t.Errorf("ListOnions after remove = %v, want [beta]", got)
	}

	// Re-add alpha; key file is retained, so address must match.
	addrA2, err := eng.AddOnion(ctx, "alpha", nil)
	if err != nil {
		t.Fatalf("re-AddOnion(alpha): %v", err)
	}
	if addrA2 != addrA {
		t.Errorf("alpha re-add address = %s, want %s", addrA2, addrA)
	}

	// Removing a missing onion is an error.
	if err := eng.RemoveOnion("ghost"); !errors.Is(err, ErrOnionNotFound) {
		t.Errorf("RemoveOnion(ghost): want ErrOnionNotFound, got %v", err)
	}
}

// TestMultiOnionPersistsAcrossEngineRestart re-creates the engine after Stop
// and verifies that AddOnion under the same name resolves to the same address
// (key was saved to disk under <dataDir>/services/<name>/onion.key).
func TestMultiOnionPersistsAcrossEngineRestart(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	eng1 := NewEngine(dir, integrationTorPath(t))
	if err := eng1.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	<-eng1.Ready()

	addr1, err := eng1.AddOnion(ctx, "node", nil)
	if err != nil {
		eng1.Stop()
		t.Fatalf("AddOnion(node): %v", err)
	}
	if err := eng1.Stop(); err != nil {
		t.Fatalf("Stop(): %v", err)
	}

	eng2 := NewEngine(dir, integrationTorPath(t))
	if err := eng2.Start(ctx); err != nil {
		t.Fatalf("Start() second: %v", err)
	}
	defer eng2.Stop()
	<-eng2.Ready()

	addr2, err := eng2.AddOnion(ctx, "node", nil)
	if err != nil {
		t.Fatalf("re-AddOnion(node): %v", err)
	}
	if addr1 != addr2 {
		t.Errorf("address changed across restart: %s -> %s", addr1, addr2)
	}
}
