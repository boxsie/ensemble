package tor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	torVersion = "15.0.5"
	torBaseURL = "https://dist.torproject.org/torbrowser/" + torVersion + "/"
)

// platformBundle maps GOOS/GOARCH to the bundle filename suffix and SHA-256 checksum.
type platformBundle struct {
	Filename string
	SHA256   string
}

var bundles = map[string]platformBundle{
	"linux/amd64": {
		Filename: "tor-expert-bundle-linux-x86_64-" + torVersion + ".tar.gz",
		SHA256:   "df5d4850779d9648160c54df1a82733ea54f3430c46b3cc3ed2a22b579f8758e",
	},
	"darwin/amd64": {
		Filename: "tor-expert-bundle-macos-x86_64-" + torVersion + ".tar.gz",
		SHA256:   "38a62c81c93eee88a273a14b8669e7d5a72583adef1c37c28e1cd98ee5ea2ade",
	},
	"darwin/arm64": {
		Filename: "tor-expert-bundle-macos-aarch64-" + torVersion + ".tar.gz",
		SHA256:   "147be0997f4538c6f20f3a113f6220f7a1c711acc3410f534f0a3cf62b7a06dd",
	},
	"windows/amd64": {
		Filename: "tor-expert-bundle-windows-x86_64-" + torVersion + ".tar.gz",
		SHA256:   "49aabfe2958c8084e9fba1f78d85049e16a657e1f679b75102bbf9518497607f",
	},
}

// torBinaryName returns the platform-specific tor binary name.
func torBinaryName() string {
	if runtime.GOOS == "windows" {
		return "tor.exe"
	}
	return "tor"
}

// bundleForPlatform returns the bundle info for the given OS/arch pair.
func bundleForPlatform(goos, goarch string) (platformBundle, error) {
	key := goos + "/" + goarch
	b, ok := bundles[key]
	if !ok {
		return platformBundle{}, fmt.Errorf("unsupported platform: %s", key)
	}
	return b, nil
}

// EnsureTor returns the path to a working tor binary, downloading the
// official Tor Expert Bundle if necessary. The binary is cached in
// dataDir/tor/bin/.
func EnsureTor(ctx context.Context, dataDir string) (string, error) {
	binDir := filepath.Join(dataDir, "tor", "bin")
	torPath := filepath.Join(binDir, torBinaryName())
	versionFile := filepath.Join(binDir, "version.txt")

	// Check if we already have the correct version cached.
	if data, err := os.ReadFile(versionFile); err == nil {
		if strings.TrimSpace(string(data)) == torVersion {
			if _, err := os.Stat(torPath); err == nil {
				return torPath, nil
			}
		}
	}

	bundle, err := bundleForPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	url := torBaseURL + bundle.Filename

	archivePath := filepath.Join(dataDir, "tor", bundle.Filename)
	if err := os.MkdirAll(filepath.Join(dataDir, "tor"), 0700); err != nil {
		return "", fmt.Errorf("creating tor directory: %w", err)
	}

	if err := downloadFile(ctx, url, archivePath); err != nil {
		return "", fmt.Errorf("downloading tor bundle: %w", err)
	}

	if err := verifyChecksum(archivePath, bundle.SHA256); err != nil {
		os.Remove(archivePath)
		return "", fmt.Errorf("checksum verification failed: %w", err)
	}

	// Remove old bin dir before extracting.
	os.RemoveAll(binDir)
	if err := os.MkdirAll(binDir, 0700); err != nil {
		return "", fmt.Errorf("creating bin directory: %w", err)
	}

	if err := extractTorBinary(archivePath, binDir); err != nil {
		os.RemoveAll(binDir)
		return "", fmt.Errorf("extracting tor binary: %w", err)
	}

	// Write version marker.
	if err := os.WriteFile(versionFile, []byte(torVersion), 0600); err != nil {
		return "", fmt.Errorf("writing version file: %w", err)
	}

	// Clean up the archive.
	os.Remove(archivePath)

	if _, err := os.Stat(torPath); err != nil {
		return "", fmt.Errorf("tor binary not found after extraction: %w", err)
	}

	return torPath, nil
}

// downloadFile downloads a URL to a local file.
func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}

	return f.Close()
}

// verifyChecksum checks that a file matches the expected SHA-256 hex digest.
func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("expected %s, got %s", expected, got)
	}
	return nil
}

// extractTorBinary extracts the tor binary (and required shared libs) from
// the Tor Expert Bundle tar.gz into destDir.
func extractTorBinary(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	binName := torBinaryName()
	found := false

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Extract from "tor/" dir: the binary and shared libs.
		// Extract from "data/" dir: geoip files.
		// Skip "debug/" and everything else.
		base := filepath.Base(hdr.Name)
		dir := strings.SplitN(hdr.Name, "/", 2)[0]

		isTorDir := dir == "tor"
		isDataDir := dir == "data"

		if !isTorDir && !isDataDir {
			continue
		}

		isTorBin := isTorDir && base == binName
		isSharedLib := isTorDir && (strings.HasSuffix(base, ".so") ||
			strings.Contains(base, ".so.") ||
			strings.HasSuffix(base, ".dylib") ||
			strings.HasSuffix(base, ".dll"))
		isGeoIP := isDataDir && (base == "geoip" || base == "geoip6")

		if !isTorBin && !isSharedLib && !isGeoIP {
			continue
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}

		// Flatten into destDir (or destDir/data for geoip).
		var outPath string
		if isGeoIP {
			outDir := filepath.Join(destDir, "data")
			if err := os.MkdirAll(outDir, 0700); err != nil {
				return err
			}
			outPath = filepath.Join(outDir, base)
		} else {
			outPath = filepath.Join(destDir, base)
		}

		// Sanitize: reject paths that escape destDir.
		if !strings.HasPrefix(filepath.Clean(outPath), filepath.Clean(destDir)) {
			continue
		}

		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)|0500)
		if err != nil {
			return fmt.Errorf("creating %s: %w", outPath, err)
		}

		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("extracting %s: %w", outPath, err)
		}
		out.Close()

		if isTorBin {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("tor binary %q not found in archive", binName)
	}
	return nil
}
