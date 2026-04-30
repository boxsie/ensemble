package testutil

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/boxsie/ensemble/internal/identity"
)

// DeterministicKeypair generates a reproducible keypair from a numeric seed.
// Same index always produces the same keypair. Useful for tests that need
// stable identities across runs.
func DeterministicKeypair(index int) *identity.Keypair {
	var seedBuf [32]byte
	binary.BigEndian.PutUint64(seedBuf[:8], uint64(index))
	// SHA-256 to spread entropy across all 32 bytes.
	seed := sha256.Sum256(seedBuf[:])
	kp, err := identity.FromSeed(seed[:])
	if err != nil {
		panic(fmt.Sprintf("testutil: generating keypair %d: %v", index, err))
	}
	return kp
}

// Alice returns a deterministic keypair for "Alice" (index 1).
func Alice() *identity.Keypair { return DeterministicKeypair(1) }

// Bob returns a deterministic keypair for "Bob" (index 2).
func Bob() *identity.Keypair { return DeterministicKeypair(2) }

// Carol returns a deterministic keypair for "Carol" (index 3).
func Carol() *identity.Keypair { return DeterministicKeypair(3) }

// AliceAddr returns Alice's ensemble address.
func AliceAddr() string { return string(identity.DeriveAddress(Alice().PublicKey())) }

// BobAddr returns Bob's ensemble address.
func BobAddr() string { return string(identity.DeriveAddress(Bob().PublicKey())) }

// CarolAddr returns Carol's ensemble address.
func CarolAddr() string { return string(identity.DeriveAddress(Carol().PublicKey())) }
