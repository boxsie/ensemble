package identity

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestGenerate(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(kp.PublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("public key size: got %d, want %d", len(kp.PublicKey()), ed25519.PublicKeySize)
	}
}

func TestGenerateUniqueness(t *testing.T) {
	kp1, _ := Generate()
	kp2, _ := Generate()
	if bytes.Equal(kp1.PublicKey(), kp2.PublicKey()) {
		t.Fatal("two generated keypairs should not have the same public key")
	}
}

func TestFromSeed(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}

	kp1, err := FromSeed(seed)
	if err != nil {
		t.Fatalf("FromSeed() error: %v", err)
	}

	// Same seed must produce the same keypair.
	kp2, err := FromSeed(seed)
	if err != nil {
		t.Fatalf("FromSeed() error: %v", err)
	}
	if !bytes.Equal(kp1.PublicKey(), kp2.PublicKey()) {
		t.Fatal("same seed should produce the same public key")
	}
}

func TestFromSeedInvalidSize(t *testing.T) {
	_, err := FromSeed([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("FromSeed() should reject invalid seed size")
	}
}

func TestSignAndVerify(t *testing.T) {
	kp, _ := Generate()
	msg := []byte("hello ensemble")

	sig := kp.Sign(msg)

	if !kp.Verify(msg, sig) {
		t.Fatal("signature should verify against the signing key")
	}
}

func TestVerifyRejectsTamperedMessage(t *testing.T) {
	kp, _ := Generate()
	msg := []byte("hello ensemble")
	sig := kp.Sign(msg)

	tampered := []byte("hello ensemblX")
	if kp.Verify(tampered, sig) {
		t.Fatal("signature should not verify against a tampered message")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	kp1, _ := Generate()
	kp2, _ := Generate()
	msg := []byte("hello ensemble")

	sig := kp1.Sign(msg)
	if kp2.Verify(msg, sig) {
		t.Fatal("signature should not verify against a different key")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	kp, _ := Generate()

	data := kp.MarshalPrivate()
	if len(data) != ed25519.PrivateKeySize {
		t.Fatalf("marshalled size: got %d, want %d", len(data), ed25519.PrivateKeySize)
	}

	restored, err := UnmarshalPrivate(data)
	if err != nil {
		t.Fatalf("UnmarshalPrivate() error: %v", err)
	}

	if !bytes.Equal(kp.PublicKey(), restored.PublicKey()) {
		t.Fatal("round-trip should preserve public key")
	}

	// Verify the restored key can sign and the original can verify.
	msg := []byte("round trip test")
	sig := restored.Sign(msg)
	if !kp.Verify(msg, sig) {
		t.Fatal("original key should verify signature from restored key")
	}
}

func TestUnmarshalPrivateInvalidSize(t *testing.T) {
	_, err := UnmarshalPrivate([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("UnmarshalPrivate() should reject invalid size")
	}
}

func TestSeed(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 42)
	}

	kp, _ := FromSeed(seed)
	if !bytes.Equal(kp.Seed(), seed) {
		t.Fatal("Seed() should return the original seed")
	}
}

func TestToLibp2pKey(t *testing.T) {
	kp, _ := Generate()

	lk, err := kp.ToLibp2pKey()
	if err != nil {
		t.Fatalf("ToLibp2pKey() error: %v", err)
	}

	// The libp2p key's raw bytes should match our private key.
	raw, err := lk.Raw()
	if err != nil {
		t.Fatalf("libp2p Raw() error: %v", err)
	}
	if !bytes.Equal(raw, kp.MarshalPrivate()) {
		t.Fatal("libp2p key raw bytes should match our private key")
	}

	// The libp2p public key should match ours.
	lpub, err := lk.GetPublic().Raw()
	if err != nil {
		t.Fatalf("libp2p GetPublic().Raw() error: %v", err)
	}
	if !bytes.Equal(lpub, kp.PublicKey()) {
		t.Fatal("libp2p public key should match our public key")
	}
}
