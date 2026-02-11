package tor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleForPlatform(t *testing.T) {
	tests := []struct {
		goos, goarch string
		wantFile     string
		wantErr      bool
	}{
		{"linux", "amd64", "tor-expert-bundle-linux-x86_64-" + torVersion + ".tar.gz", false},
		{"darwin", "amd64", "tor-expert-bundle-macos-x86_64-" + torVersion + ".tar.gz", false},
		{"darwin", "arm64", "tor-expert-bundle-macos-aarch64-" + torVersion + ".tar.gz", false},
		{"windows", "amd64", "tor-expert-bundle-windows-x86_64-" + torVersion + ".tar.gz", false},
		{"linux", "arm64", "", true},
		{"freebsd", "amd64", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			b, err := bundleForPlatform(tt.goos, tt.goarch)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if b.Filename != tt.wantFile {
				t.Errorf("filename = %q, want %q", b.Filename, tt.wantFile)
			}
			if len(b.SHA256) != 64 {
				t.Errorf("SHA256 length = %d, want 64", len(b.SHA256))
			}
		})
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello tor bundle")
	path := filepath.Join(dir, "test.tar.gz")

	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(data)
	goodHash := hex.EncodeToString(h[:])

	// Should pass with correct hash.
	if err := verifyChecksum(path, goodHash); err != nil {
		t.Fatalf("verifyChecksum with correct hash: %v", err)
	}

	// Should fail with wrong hash.
	badHash := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := verifyChecksum(path, badHash); err == nil {
		t.Fatal("verifyChecksum with wrong hash: expected error")
	}
}

func TestEnsureTorUsesCached(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "tor", "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create a fake tor binary.
	binName := torBinaryName()
	torPath := filepath.Join(binDir, binName)
	if err := os.WriteFile(torPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Write the version marker matching the current version.
	versionFile := filepath.Join(binDir, "version.txt")
	if err := os.WriteFile(versionFile, []byte(torVersion), 0600); err != nil {
		t.Fatal(err)
	}

	// EnsureTor should return the cached path without downloading.
	got, err := EnsureTor(context.Background(), dir)
	if err != nil {
		t.Fatalf("EnsureTor() error: %v", err)
	}
	if got != torPath {
		t.Errorf("EnsureTor() = %q, want %q", got, torPath)
	}
}


func TestChecksumHexLength(t *testing.T) {
	// Verify all hardcoded checksums are valid hex and 64 chars.
	for key, b := range bundles {
		if len(b.SHA256) != 64 {
			t.Errorf("bundle %s: SHA256 length = %d, want 64", key, len(b.SHA256))
		}
		if _, err := hex.DecodeString(b.SHA256); err != nil {
			t.Errorf("bundle %s: invalid hex: %v", key, err)
		}
	}
}
