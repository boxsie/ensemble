package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	saltSize  = 16
	nonceSize = 12 // AES-GCM standard nonce
	keySize   = 32 // AES-256

	// Argon2id parameters.
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MB
	argonThreads = 4

	// NodeService is the reserved name for the daemon's primary identity.
	NodeService = "node"

	servicesDir = "services"
	keyFileName = "identity.key"
	legacyKey   = "identity.key"
)

// ErrNotFound is returned when a service has no keypair in the keystore.
var ErrNotFound = errors.New("identity: service not found")

// ErrAlreadyExists is returned by Generate when a keypair already exists for the name.
var ErrAlreadyExists = errors.New("identity: service already has a keypair")

var nameRegexp = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

// Keystore holds N encrypted Ed25519 keypairs indexed by service name.
//
// Layout on disk:
//
//	<dir>/services/<name>/identity.key
//
// A legacy single-key file at <dir>/identity.key is migrated to
// <dir>/services/node/identity.key on first construction.
type Keystore struct {
	dir        string
	passphrase string

	mu    sync.RWMutex
	cache map[string]*Keypair
}

// NewKeystore creates a keystore rooted at dir. The passphrase is applied to
// every keypair persisted through this keystore. If a legacy <dir>/identity.key
// exists and <dir>/services/node/identity.key does not, the legacy file is
// moved to the new location.
func NewKeystore(dir, passphrase string) (*Keystore, error) {
	ks := &Keystore{
		dir:        dir,
		passphrase: passphrase,
		cache:      make(map[string]*Keypair),
	}
	if err := ks.migrateLegacy(); err != nil {
		return nil, fmt.Errorf("migrating legacy identity: %w", err)
	}
	return ks, nil
}

// Get returns the keypair for the given service. The keypair is decrypted on
// first access and cached for subsequent calls.
func (ks *Keystore) Get(name string) (*Keypair, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	ks.mu.RLock()
	if kp, ok := ks.cache[name]; ok {
		ks.mu.RUnlock()
		return kp, nil
	}
	ks.mu.RUnlock()

	ks.mu.Lock()
	defer ks.mu.Unlock()

	if kp, ok := ks.cache[name]; ok {
		return kp, nil
	}

	kp, err := ks.loadFromDisk(name)
	if err != nil {
		return nil, err
	}
	ks.cache[name] = kp
	return kp, nil
}

// Generate creates a new keypair for the given service, persists it, and
// returns it. Returns ErrAlreadyExists if a keypair is already stored under
// that name.
func (ks *Keystore) Generate(name string) (*Keypair, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	if _, ok := ks.cache[name]; ok {
		return nil, ErrAlreadyExists
	}
	if existsOnDisk(ks.keyPath(name)) {
		return nil, ErrAlreadyExists
	}

	kp, err := Generate()
	if err != nil {
		return nil, err
	}

	if err := ks.saveToDisk(name, kp); err != nil {
		return nil, err
	}
	ks.cache[name] = kp
	return kp, nil
}

// GetOrGenerate returns the existing keypair for name, or generates and
// persists a new one if none is stored.
func (ks *Keystore) GetOrGenerate(name string) (*Keypair, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	if kp, ok := ks.cache[name]; ok {
		return kp, nil
	}

	if existsOnDisk(ks.keyPath(name)) {
		kp, err := ks.loadFromDisk(name)
		if err != nil {
			return nil, err
		}
		ks.cache[name] = kp
		return kp, nil
	}

	kp, err := Generate()
	if err != nil {
		return nil, err
	}
	if err := ks.saveToDisk(name, kp); err != nil {
		return nil, err
	}
	ks.cache[name] = kp
	return kp, nil
}

// Has reports whether a keypair is stored on disk for the given service.
// Returns false for invalid names.
func (ks *Keystore) Has(name string) bool {
	if err := validateName(name); err != nil {
		return false
	}
	ks.mu.RLock()
	if _, ok := ks.cache[name]; ok {
		ks.mu.RUnlock()
		return true
	}
	ks.mu.RUnlock()
	return existsOnDisk(ks.keyPath(name))
}

// List returns the sorted set of service names that have a keypair on disk.
func (ks *Keystore) List() []string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	root := filepath.Join(ks.dir, servicesDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if validateName(name) != nil {
			continue
		}
		if !existsOnDisk(filepath.Join(root, name, keyFileName)) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Remove deletes the on-disk keypair for the given service and drops the
// in-memory cache entry. Returns ErrNotFound if no keypair exists.
func (ks *Keystore) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}

	ks.mu.Lock()
	defer ks.mu.Unlock()

	path := ks.keyPath(name)
	if !existsOnDisk(path) {
		if _, ok := ks.cache[name]; !ok {
			return ErrNotFound
		}
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing key file: %w", err)
	}
	// Best-effort: remove the now-empty service directory.
	_ = os.Remove(filepath.Dir(path))

	delete(ks.cache, name)
	return nil
}

// keyPath returns the on-disk path for a service's identity file.
func (ks *Keystore) keyPath(name string) string {
	return filepath.Join(ks.dir, servicesDir, name, keyFileName)
}

// migrateLegacy moves <dir>/identity.key to <dir>/services/node/identity.key
// if the legacy file exists and the new file does not.
func (ks *Keystore) migrateLegacy() error {
	legacy := filepath.Join(ks.dir, legacyKey)
	target := ks.keyPath(NodeService)

	if !existsOnDisk(legacy) {
		return nil
	}
	if existsOnDisk(target) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return fmt.Errorf("creating services directory: %w", err)
	}
	if err := os.Rename(legacy, target); err != nil {
		return fmt.Errorf("renaming legacy key file: %w", err)
	}
	log.Printf("identity: migrated legacy key %s -> %s", legacy, target)
	return nil
}

// loadFromDisk decrypts the keypair stored for name. Returns ErrNotFound if
// the file does not exist.
func (ks *Keystore) loadFromDisk(name string) (*Keypair, error) {
	path := ks.keyPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	if len(data) < saltSize+nonceSize {
		return nil, fmt.Errorf("key file too short")
	}

	salt := data[:saltSize]
	nonce := data[saltSize : saltSize+nonceSize]
	ciphertext := data[saltSize+nonceSize:]

	aesKey := argon2.IDKey([]byte(ks.passphrase), salt, argonTime, argonMemory, argonThreads, keySize)

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting key (wrong passphrase?): %w", err)
	}

	return UnmarshalPrivate(plaintext)
}

// saveToDisk encrypts and writes the keypair for name. The caller must hold
// the keystore write lock.
func (ks *Keystore) saveToDisk(name string, kp *Keypair) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}

	aesKey := argon2.IDKey([]byte(ks.passphrase), salt, argonTime, argonMemory, argonThreads, keySize)

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("creating GCM: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, kp.MarshalPrivate(), nil)

	out := make([]byte, 0, saltSize+nonceSize+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	path := ks.keyPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		return fmt.Errorf("writing key file: %w", err)
	}
	return nil
}

// validateName enforces slug-safe service names.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("identity: service name must not be empty")
	}
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("identity: invalid service name %q (lowercase alphanumeric, '-' and '_' only)", name)
	}
	return nil
}

func existsOnDisk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
