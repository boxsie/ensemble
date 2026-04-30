package tor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureTorDownload(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	torPath, err := EnsureTor(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureTor() error: %v", err)
	}

	// Binary should exist and be executable.
	info, err := os.Stat(torPath)
	if err != nil {
		t.Fatalf("stat tor binary: %v", err)
	}
	if info.Mode()&0100 == 0 {
		t.Errorf("tor binary is not executable: mode %v", info.Mode())
	}
	t.Logf("tor binary: %s (%d bytes)", torPath, info.Size())

	// Version file should match.
	versionData, err := os.ReadFile(filepath.Join(dir, "tor", "bin", "version.txt"))
	if err != nil {
		t.Fatalf("reading version.txt: %v", err)
	}
	if got := string(versionData); got != torVersion {
		t.Errorf("version.txt = %q, want %q", got, torVersion)
	}

	// Archive should have been cleaned up.
	bundle, _ := bundleForPlatform("linux", "amd64")
	archivePath := filepath.Join(dir, "tor", bundle.Filename)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Errorf("archive not cleaned up: %s", archivePath)
	}

	// Second call should use cache (fast).
	start := time.Now()
	torPath2, err := EnsureTor(ctx, dir)
	if err != nil {
		t.Fatalf("second EnsureTor() error: %v", err)
	}
	if torPath2 != torPath {
		t.Errorf("cached path differs: %q vs %q", torPath2, torPath)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cached lookup took %v, expected < 1s", elapsed)
	}
}

func TestEnsureTorStaleVersionRedownloads(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "tor", "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Seed with a stale version.
	if err := os.WriteFile(filepath.Join(binDir, torBinaryName()), []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "version.txt"), []byte("0.0.0"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	torPath, err := EnsureTor(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureTor() error: %v", err)
	}

	// Should now have the real binary, not our "old" stub.
	info, err := os.Stat(torPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() < 1000 {
		t.Errorf("tor binary suspiciously small (%d bytes), expected real binary", info.Size())
	}

	versionData, _ := os.ReadFile(filepath.Join(binDir, "version.txt"))
	if got := string(versionData); got != torVersion {
		t.Errorf("version.txt = %q, want %q", got, torVersion)
	}
}
