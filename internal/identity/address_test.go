package identity

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestDeriveAddressStartsWithE(t *testing.T) {
	kp, _ := Generate()
	addr := DeriveAddress(kp.PublicKey())
	if !strings.HasPrefix(string(addr), "E") {
		t.Fatalf("address should start with E, got %q", addr)
	}
}

func TestDeriveAddressDeterministic(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	kp, _ := FromSeed(seed)

	addr1 := DeriveAddress(kp.PublicKey())
	addr2 := DeriveAddress(kp.PublicKey())
	if addr1 != addr2 {
		t.Fatalf("same key should produce same address: %q vs %q", addr1, addr2)
	}
}

func TestDeriveAddressUniquePerKey(t *testing.T) {
	kp1, _ := Generate()
	kp2, _ := Generate()
	a1 := DeriveAddress(kp1.PublicKey())
	a2 := DeriveAddress(kp2.PublicKey())
	if a1 == a2 {
		t.Fatal("different keys should produce different addresses")
	}
}

func TestValidateAcceptsValid(t *testing.T) {
	kp, _ := Generate()
	addr := DeriveAddress(kp.PublicKey())
	if !Validate(string(addr)) {
		t.Fatalf("valid address should pass validation: %q", addr)
	}
}

func TestValidateRejectsTampered(t *testing.T) {
	kp, _ := Generate()
	addr := string(DeriveAddress(kp.PublicKey()))

	// Flip a character in the middle.
	tampered := []byte(addr)
	if tampered[len(tampered)/2] == 'A' {
		tampered[len(tampered)/2] = 'B'
	} else {
		tampered[len(tampered)/2] = 'A'
	}
	if Validate(string(tampered)) {
		t.Fatalf("tampered address should fail validation: %q", string(tampered))
	}
}

func TestValidateRejectsEmpty(t *testing.T) {
	if Validate("") {
		t.Fatal("empty string should fail validation")
	}
}

func TestValidateRejectsGarbage(t *testing.T) {
	if Validate("not-a-real-address!!!") {
		t.Fatal("garbage input should fail validation")
	}
}

func TestMatchesPublicKey(t *testing.T) {
	kp, _ := Generate()
	addr := DeriveAddress(kp.PublicKey())

	if !MatchesPublicKey(string(addr), kp.PublicKey()) {
		t.Fatal("address should match its own public key")
	}
}

func TestMatchesPublicKeyRejectsWrongKey(t *testing.T) {
	kp1, _ := Generate()
	kp2, _ := Generate()
	addr := DeriveAddress(kp1.PublicKey())

	if MatchesPublicKey(string(addr), kp2.PublicKey()) {
		t.Fatal("address should not match a different public key")
	}
}

func TestShort(t *testing.T) {
	kp, _ := Generate()
	addr := DeriveAddress(kp.PublicKey())
	short := addr.Short()

	if !strings.HasPrefix(short, string(addr)[:6]) {
		t.Fatalf("Short() should start with first 6 chars, got %q", short)
	}
	if !strings.Contains(short, "...") {
		t.Fatalf("Short() should contain '...', got %q", short)
	}
	if !strings.HasSuffix(short, string(addr)[len(string(addr))-4:]) {
		t.Fatalf("Short() should end with last 4 chars, got %q", short)
	}
}

func TestString(t *testing.T) {
	kp, _ := Generate()
	addr := DeriveAddress(kp.PublicKey())
	if addr.String() != string(addr) {
		t.Fatal("String() should return the raw address string")
	}
}
