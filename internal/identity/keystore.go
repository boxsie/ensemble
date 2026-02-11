package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

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
)

// Keystore handles encrypted keypair persistence.
type Keystore struct {
	path string // full path to the key file
}

// NewKeystore creates a keystore that reads/writes to the given directory.
// The key file will be stored as "identity.key" inside dir.
func NewKeystore(dir string) *Keystore {
	return &Keystore{path: filepath.Join(dir, "identity.key")}
}

// Exists returns true if a key file is present on disk.
func (ks *Keystore) Exists() bool {
	_, err := os.Stat(ks.path)
	return err == nil
}

// Save encrypts and writes a keypair to disk.
// File format: salt(16) || nonce(12) || ciphertext.
func (ks *Keystore) Save(kp *Keypair, passphrase string) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}

	aesKey := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keySize)

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return fmt.Errorf("creating cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("creating GCM: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, kp.MarshalPrivate(), nil)

	// salt || nonce || ciphertext
	out := make([]byte, 0, saltSize+nonceSize+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	if err := os.MkdirAll(filepath.Dir(ks.path), 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	if err := os.WriteFile(ks.path, out, 0600); err != nil {
		return fmt.Errorf("writing key file: %w", err)
	}

	return nil
}

// Load reads and decrypts a keypair from disk.
func (ks *Keystore) Load(passphrase string) (*Keypair, error) {
	data, err := os.ReadFile(ks.path)
	if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	if len(data) < saltSize+nonceSize {
		return nil, fmt.Errorf("key file too short")
	}

	salt := data[:saltSize]
	nonce := data[saltSize : saltSize+nonceSize]
	ciphertext := data[saltSize+nonceSize:]

	aesKey := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keySize)

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
