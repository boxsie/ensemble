package tor

import (
	"context"
	"testing"
	"time"
)

func TestEngineStartStop(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir, "")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer eng.Stop()

	// Wait for at least one progress event.
	select {
	case evt := <-eng.ProgressChan():
		if evt.Percent < 0 || evt.Percent > 100 {
			t.Errorf("unexpected progress percent: %d", evt.Percent)
		}
		t.Logf("progress: %d%% — %s", evt.Percent, evt.Summary)
	case <-ctx.Done():
		t.Fatal("timed out waiting for bootstrap progress")
	}

	// Wait for ready.
	select {
	case <-eng.Ready():
		t.Log("tor is ready")
	case <-ctx.Done():
		t.Fatal("timed out waiting for tor to be ready")
	}

	addr := eng.SOCKSAddr()
	if addr == "" {
		t.Error("SOCKSAddr() is empty after bootstrap")
	}
	t.Logf("SOCKS address: %s", addr)

	// Stop should not error.
	if err := eng.Stop(); err != nil {
		t.Errorf("Stop() error: %v", err)
	}

	// Double stop should be safe.
	if err := eng.Stop(); err != nil {
		t.Errorf("double Stop() error: %v", err)
	}
}

func TestOnionServicePersistence(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir, "")

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer eng.Stop()

	<-eng.Ready()

	keyPath := dir + "/onion.pem"

	// First creation — generates key.
	svc, err := eng.CreateOnionService(ctx, 9999, keyPath)
	if err != nil {
		t.Fatalf("CreateOnionService() error: %v", err)
	}
	addr1 := svc.OnionAddr()
	t.Logf("onion address: %s", addr1)
	if addr1 == "" || addr1 == ".onion" {
		t.Fatal("empty onion address")
	}
	svc.Close()

	// Second creation — should reuse key and get same address.
	svc2, err := eng.CreateOnionService(ctx, 9999, keyPath)
	if err != nil {
		t.Fatalf("second CreateOnionService() error: %v", err)
	}
	addr2 := svc2.OnionAddr()
	svc2.Close()

	if addr1 != addr2 {
		t.Errorf("onion addresses differ: %s vs %s", addr1, addr2)
	}
}
