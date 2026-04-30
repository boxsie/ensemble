package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
)

// Keypair wraps an Ed25519 key pair.
type Keypair struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// Generate creates a new random Ed25519 keypair.
func Generate() (*Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating keypair: %w", err)
	}
	return &Keypair{pub: pub, priv: priv}, nil
}

// FromSeed creates a keypair deterministically from a 32-byte seed.
func FromSeed(seed []byte) (*Keypair, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid seed size: got %d, want %d", len(seed), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Keypair{pub: pub, priv: priv}, nil
}

// PublicKey returns the Ed25519 public key.
func (k *Keypair) PublicKey() ed25519.PublicKey {
	return k.pub
}

// Seed returns the 32-byte seed from which the private key was derived.
func (k *Keypair) Seed() []byte {
	return k.priv.Seed()
}

// Sign signs a message and returns the signature.
func (k *Keypair) Sign(message []byte) []byte {
	return ed25519.Sign(k.priv, message)
}

// Verify checks a signature against this keypair's public key.
func (k *Keypair) Verify(message, signature []byte) bool {
	return ed25519.Verify(k.pub, message, signature)
}

// MarshalPrivate returns the raw 64-byte Ed25519 private key.
func (k *Keypair) MarshalPrivate() []byte {
	out := make([]byte, ed25519.PrivateKeySize)
	copy(out, k.priv)
	return out
}

// UnmarshalPrivate restores a keypair from a raw 64-byte Ed25519 private key.
func UnmarshalPrivate(data []byte) (*Keypair, error) {
	if len(data) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: got %d, want %d", len(data), ed25519.PrivateKeySize)
	}
	priv := make(ed25519.PrivateKey, ed25519.PrivateKeySize)
	copy(priv, data)
	pub := priv.Public().(ed25519.PublicKey)
	return &Keypair{pub: pub, priv: priv}, nil
}

// FromPublicKey creates a verify-only keypair from a raw 32-byte public key.
func FromPublicKey(data []byte) (*Keypair, error) {
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: got %d, want %d", len(data), ed25519.PublicKeySize)
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, data)
	return &Keypair{pub: pub}, nil
}

// ToLibp2pKey converts the private key to a libp2p crypto.PrivKey.
func (k *Keypair) ToLibp2pKey() (libp2pcrypto.PrivKey, error) {
	lk, err := libp2pcrypto.UnmarshalEd25519PrivateKey(k.priv)
	if err != nil {
		return nil, fmt.Errorf("converting to libp2p key: %w", err)
	}
	return lk, nil
}
