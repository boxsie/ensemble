package testutil

import (
	"testing"

	"github.com/boxsie/ensemble/internal/identity"
)

func TestDeterministicKeypair(t *testing.T) {
	a1 := DeterministicKeypair(42)
	a2 := DeterministicKeypair(42)

	if string(a1.PublicKey()) != string(a2.PublicKey()) {
		t.Fatal("same index should produce same keypair")
	}

	b := DeterministicKeypair(43)
	if string(a1.PublicKey()) == string(b.PublicKey()) {
		t.Fatal("different indices should produce different keypairs")
	}
}

func TestFixedIdentities(t *testing.T) {
	alice := Alice()
	bob := Bob()
	carol := Carol()

	// All different.
	keys := [][]byte{alice.PublicKey(), bob.PublicKey(), carol.PublicKey()}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if string(keys[i]) == string(keys[j]) {
				t.Fatalf("keypairs %d and %d should be different", i, j)
			}
		}
	}

	// Deterministic.
	if string(Alice().PublicKey()) != string(alice.PublicKey()) {
		t.Fatal("Alice() not deterministic")
	}
}

func TestFixedAddresses(t *testing.T) {
	addr := AliceAddr()
	if !identity.Validate(addr) {
		t.Fatalf("AliceAddr() is not a valid address: %s", addr)
	}
	if addr[0] != 'E' {
		t.Fatalf("AliceAddr() should start with E, got %c", addr[0])
	}

	// Deterministic.
	if AliceAddr() != addr {
		t.Fatal("AliceAddr() not deterministic")
	}
}

func TestAddr(t *testing.T) {
	kp := Alice()
	addr := Addr(kp)
	if addr != AliceAddr() {
		t.Fatalf("Addr(Alice()) = %s, want %s", addr, AliceAddr())
	}
}
