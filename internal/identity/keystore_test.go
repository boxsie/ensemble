package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeystore(dir)

	kp, _ := Generate()
	if err := ks.Save(kp, "test-passphrase"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := ks.Load("test-passphrase")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !bytes.Equal(kp.PublicKey(), loaded.PublicKey()) {
		t.Fatal("loaded key should match the saved key")
	}

	// Verify the loaded key can sign and the original can verify.
	msg := []byte("keystore round trip")
	sig := loaded.Sign(msg)
	if !kp.Verify(msg, sig) {
		t.Fatal("original key should verify signature from loaded key")
	}
}

func TestLoadWrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeystore(dir)

	kp, _ := Generate()
	if err := ks.Save(kp, "correct"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	_, err := ks.Load("wrong")
	if err == nil {
		t.Fatal("Load() with wrong passphrase should fail")
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeystore(dir)

	if ks.Exists() {
		t.Fatal("Exists() should be false before saving")
	}

	kp, _ := Generate()
	if err := ks.Save(kp, "pass"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if !ks.Exists() {
		t.Fatal("Exists() should be true after saving")
	}
}

func TestLoadNoFile(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeystore(dir)

	_, err := ks.Load("pass")
	if err == nil {
		t.Fatal("Load() should fail when no file exists")
	}
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeystore(dir)

	kp, _ := Generate()
	if err := ks.Save(kp, "pass"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "identity.key"))
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Fatalf("key file permissions: got %o, want 0600", perm)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	ks := NewKeystore(dir)

	kp, _ := Generate()
	if err := ks.Save(kp, "pass"); err != nil {
		t.Fatalf("Save() should create missing directories: %v", err)
	}

	if !ks.Exists() {
		t.Fatal("key file should exist after save")
	}
}

func TestOverwrite(t *testing.T) {
	dir := t.TempDir()
	ks := NewKeystore(dir)

	kp1, _ := Generate()
	if err := ks.Save(kp1, "pass1"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	kp2, _ := Generate()
	if err := ks.Save(kp2, "pass2"); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Should load the second key with the second passphrase.
	loaded, err := ks.Load("pass2")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !bytes.Equal(kp2.PublicKey(), loaded.PublicKey()) {
		t.Fatal("should load the most recently saved key")
	}

	// First passphrase should no longer work.
	_, err = ks.Load("pass1")
	if err == nil {
		t.Fatal("old passphrase should fail after overwrite")
	}
}
