package tor

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	bineed25519 "github.com/cretz/bine/torutil/ed25519"
)

func TestAddOnionRequiresStartedEngine(t *testing.T) {
	eng := NewEngine(t.TempDir(), "")
	if _, err := eng.AddOnion(context.Background(), "node", nil); err == nil {
		t.Fatal("AddOnion on unstarted engine: expected error, got nil")
	}
}

func TestAddOnionRejectsEmptyName(t *testing.T) {
	eng := NewEngine(t.TempDir(), "")
	if _, err := eng.AddOnion(context.Background(), "", nil); err == nil {
		t.Fatal("AddOnion with empty name: expected error, got nil")
	}
}

func TestRemoveOnionMissing(t *testing.T) {
	eng := NewEngine(t.TempDir(), "")
	if err := eng.RemoveOnion("nope"); !errors.Is(err, ErrOnionNotFound) {
		t.Fatalf("RemoveOnion missing: want ErrOnionNotFound, got %v", err)
	}
}

func TestOnionAddrAndListOnEmptyEngine(t *testing.T) {
	eng := NewEngine(t.TempDir(), "")
	if got, ok := eng.OnionAddr("anything"); ok || got != "" {
		t.Fatalf("OnionAddr on empty: got (%q,%v), want (\"\",false)", got, ok)
	}
	if got := eng.ListOnions(); len(got) != 0 {
		t.Fatalf("ListOnions on empty: %v, want empty", got)
	}
}

func TestOnionKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "services", "node", "onion.key")

	// Generate a real bine ed25519 keypair.
	kp, err := bineed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if err := saveOnionKey(keyPath, kp); err != nil {
		t.Fatalf("saveOnionKey: %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat saved key: %v", err)
	}
	if info.Size() != 96 {
		t.Errorf("saved key size = %d, want 96", info.Size())
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("saved key mode = %o, want 0600", mode)
	}

	loadedAny, err := loadOnionKey(keyPath)
	if err != nil {
		t.Fatalf("loadOnionKey: %v", err)
	}
	loaded, ok := loadedAny.(bineed25519.KeyPair)
	if !ok {
		t.Fatalf("loaded key has wrong type: %T", loadedAny)
	}
	if !reflect.DeepEqual([]byte(loaded.PublicKey()), []byte(kp.PublicKey())) {
		t.Errorf("loaded public key does not match original")
	}
	if !reflect.DeepEqual([]byte(loaded.PrivateKey()), []byte(kp.PrivateKey())) {
		t.Errorf("loaded private key does not match original")
	}
}

func TestOnionKeyLoadMissingReturnsNotExist(t *testing.T) {
	_, err := loadOnionKey(filepath.Join(t.TempDir(), "nope.key"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loadOnionKey on missing: want ErrNotExist, got %v", err)
	}
}

func TestOnionKeyLoadRejectsWrongSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(path, []byte("too short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOnionKey(path); err == nil {
		t.Fatal("loadOnionKey with wrong-size file: expected error")
	}
}
