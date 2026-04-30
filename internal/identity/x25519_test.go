package identity

import (
	"bytes"
	"crypto/ecdh"
	"testing"
)

func TestToX25519PrivateKeyLength(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	xPriv := kp.ToX25519PrivateKey()
	if len(xPriv) != 32 {
		t.Fatalf("got %d bytes, want 32", len(xPriv))
	}
}

func TestToX25519PublicKeyLength(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	xPub, err := kp.ToX25519PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(xPub) != 32 {
		t.Fatalf("got %d bytes, want 32", len(xPub))
	}
}

func TestX25519ConversionConsistency(t *testing.T) {
	// The X25519 public key derived from the converted private key must
	// match the birational-map conversion of the Ed25519 public key.
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}

	xPriv := kp.ToX25519PrivateKey()
	xPub, err := kp.ToX25519PublicKey()
	if err != nil {
		t.Fatal(err)
	}

	curve := ecdh.X25519()
	privKey, err := curve.NewPrivateKey(xPriv)
	if err != nil {
		t.Fatalf("creating ecdh private key: %v", err)
	}
	derivedPub := privKey.PublicKey().Bytes()

	if !bytes.Equal(xPub, derivedPub) {
		t.Fatalf("birational-map pub %x != derived pub %x", xPub, derivedPub)
	}
}

func TestX25519DeterministicFromSeed(t *testing.T) {
	seed := make([]byte, 32)
	seed[0] = 42

	kp1, _ := FromSeed(seed)
	kp2, _ := FromSeed(seed)

	if !bytes.Equal(kp1.ToX25519PrivateKey(), kp2.ToX25519PrivateKey()) {
		t.Fatal("same seed produced different X25519 private keys")
	}
	pub1, _ := kp1.ToX25519PublicKey()
	pub2, _ := kp2.ToX25519PublicKey()
	if !bytes.Equal(pub1, pub2) {
		t.Fatal("same seed produced different X25519 public keys")
	}
}

func TestEd25519PublicKeyToX25519InvalidLength(t *testing.T) {
	_, err := Ed25519PublicKeyToX25519([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for invalid key length")
	}
}
