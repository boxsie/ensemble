package identity

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// newKS is a tiny helper to keep tests readable.
func newKS(t *testing.T, dir, pass string) *Keystore {
	t.Helper()
	ks, err := NewKeystore(dir, pass)
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	return ks
}

func TestKeystoreGenerateAndGet(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	kp, err := ks.Generate("node")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := ks.Get("node")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(kp.PublicKey(), got.PublicKey()) {
		t.Fatal("Get returned a different keypair than Generate")
	}

	// Confirm on-disk path.
	want := filepath.Join(dir, "services", "node", "identity.key")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected key file at %s: %v", want, err)
	}
}

func TestKeystoreGetOrGenerate(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	first, err := ks.GetOrGenerate("node")
	if err != nil {
		t.Fatalf("GetOrGenerate (first): %v", err)
	}
	second, err := ks.GetOrGenerate("node")
	if err != nil {
		t.Fatalf("GetOrGenerate (second): %v", err)
	}
	if !bytes.Equal(first.PublicKey(), second.PublicKey()) {
		t.Fatal("GetOrGenerate should be idempotent")
	}

	// Re-open keystore and confirm we still load the same key.
	ks2 := newKS(t, dir, "pass")
	loaded, err := ks2.Get("node")
	if err != nil {
		t.Fatalf("Get after re-open: %v", err)
	}
	if !bytes.Equal(first.PublicKey(), loaded.PublicKey()) {
		t.Fatal("identity should persist across keystore instances")
	}
}

func TestKeystoreHas(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if ks.Has("node") {
		t.Fatal("Has should be false before Generate")
	}
	if _, err := ks.Generate("node"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !ks.Has("node") {
		t.Fatal("Has should be true after Generate")
	}
	if ks.Has("absent") {
		t.Fatal("Has should be false for unknown service")
	}
}

func TestKeystoreList(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if got := ks.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %v", got)
	}

	for _, name := range []string{"node", "jarvis", "ci_bot"} {
		if _, err := ks.Generate(name); err != nil {
			t.Fatalf("Generate(%q): %v", name, err)
		}
	}

	got := ks.List()
	want := []string{"ci_bot", "jarvis", "node"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("List length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List[%d]: got %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
}

func TestKeystoreRemove(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if _, err := ks.Generate("jarvis"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := ks.Remove("jarvis"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ks.Has("jarvis") {
		t.Fatal("Has should be false after Remove")
	}

	// Removing again must return ErrNotFound.
	if err := ks.Remove("jarvis"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove of missing should return ErrNotFound, got %v", err)
	}
}

func TestKeystoreGetMissing(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if _, err := ks.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// Generate-of-existing-name behavior: documented as ErrAlreadyExists (not idempotent).
// Use GetOrGenerate for the idempotent boot path.
func TestKeystoreGenerateOfExistingErrors(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if _, err := ks.Generate("node"); err != nil {
		t.Fatalf("Generate (first): %v", err)
	}
	if _, err := ks.Generate("node"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("Generate (second) should return ErrAlreadyExists, got %v", err)
	}

	// Same outcome through a fresh keystore that picks up the on-disk file.
	ks2 := newKS(t, dir, "pass")
	if _, err := ks2.Generate("node"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("Generate after re-open should return ErrAlreadyExists, got %v", err)
	}
}

func TestKeystoreNameValidation(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	bad := []string{"", "Node", "has space", "../escape", "trailing-", "_leading", "name!"}
	for _, name := range bad {
		if _, err := ks.Generate(name); err == nil {
			t.Errorf("Generate(%q) should reject invalid name", name)
		}
		if _, err := ks.Get(name); err == nil {
			t.Errorf("Get(%q) should reject invalid name", name)
		}
		if ks.Has(name) {
			t.Errorf("Has(%q) should be false for invalid name", name)
		}
	}

	good := []string{"node", "jarvis", "ci_bot", "svc-1", "a"}
	for _, name := range good {
		if _, err := ks.Generate(name); err != nil {
			t.Errorf("Generate(%q) should accept valid name: %v", name, err)
		}
	}
}

func TestKeystoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if _, err := ks.Generate("node"); err != nil {
		t.Fatalf("seed Generate: %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range 32 {
				if _, err := ks.Get("node"); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				_ = ks.Has("node")
				_ = ks.List()
			}
		}()
	}

	// Concurrently generate distinct services.
	wg.Add(workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			name := concurrentName(i)
			if _, err := ks.GetOrGenerate(name); err != nil {
				t.Errorf("GetOrGenerate(%q): %v", name, err)
			}
		}()
	}

	wg.Wait()
}

func concurrentName(i int) string {
	letters := []byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h',
		'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p'}
	return "svc_" + string(letters[i%len(letters)])
}

// TestKeystoreMigratesLegacyFile prepopulates the old single-key file and
// asserts it is moved to the per-service layout with byte-identical content.
func TestKeystoreMigratesLegacyFile(t *testing.T) {
	dir := t.TempDir()

	// Step 1: stand up an old-style keystore by writing a file at <dir>/identity.key.
	// Use the new keystore's encryption path by saving to a temp dir, then renaming.
	scratch := t.TempDir()
	seed := newKS(t, scratch, "legacy-pass")
	original, err := seed.Generate("node")
	if err != nil {
		t.Fatalf("seed Generate: %v", err)
	}
	srcPath := filepath.Join(scratch, "services", "node", "identity.key")
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read seed key: %v", err)
	}
	legacyPath := filepath.Join(dir, "identity.key")
	if err := os.WriteFile(legacyPath, srcBytes, 0600); err != nil {
		t.Fatalf("write legacy key: %v", err)
	}

	// Step 2: open a keystore at dir; migration should fire on construction.
	ks := newKS(t, dir, "legacy-pass")

	newPath := filepath.Join(dir, "services", "node", "identity.key")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected migrated key at %s: %v", newPath, err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy file should be gone after migration, stat err=%v", err)
	}

	migratedBytes, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read migrated key: %v", err)
	}
	if !bytes.Equal(srcBytes, migratedBytes) {
		t.Fatal("migrated key bytes should match the legacy file exactly")
	}

	// And the loaded keypair must match the original.
	loaded, err := ks.Get("node")
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if !bytes.Equal(original.PublicKey(), loaded.PublicKey()) {
		t.Fatal("public key should survive migration unchanged")
	}
}

// TestKeystoreMigrationSkippedWhenTargetExists ensures we never clobber a
// per-service key that already exists.
func TestKeystoreMigrationSkippedWhenTargetExists(t *testing.T) {
	dir := t.TempDir()

	// Create a per-service key first.
	ks1 := newKS(t, dir, "pass")
	want, err := ks1.Generate("node")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Then plant a stray legacy file. It must be left untouched.
	legacyPath := filepath.Join(dir, "identity.key")
	stray := []byte("not a real key file")
	if err := os.WriteFile(legacyPath, stray, 0600); err != nil {
		t.Fatalf("write stray legacy: %v", err)
	}

	ks2 := newKS(t, dir, "pass")
	got, err := ks2.Get("node")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(want.PublicKey(), got.PublicKey()) {
		t.Fatal("existing per-service key must not be overwritten")
	}

	gotLegacy, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if !bytes.Equal(stray, gotLegacy) {
		t.Fatal("legacy file should be left untouched when target exists")
	}
}

func TestKeystoreFilePermissions(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "pass")

	if _, err := ks.Generate("node"); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "services", "node", "identity.key"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("key file permissions: got %o, want 0600", perm)
	}
}

func TestKeystoreWrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	ks := newKS(t, dir, "correct")
	if _, err := ks.Generate("node"); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	other, err := NewKeystore(dir, "wrong")
	if err != nil {
		t.Fatalf("NewKeystore: %v", err)
	}
	if _, err := other.Get("node"); err == nil {
		t.Fatal("Get with wrong passphrase should fail")
	}
}
