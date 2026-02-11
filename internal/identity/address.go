package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"

	"github.com/mr-tron/base58"
	"github.com/decred/dcrd/crypto/ripemd160"
)

// VersionByte is the address version prefix. 0x21 produces addresses starting with "E".
const VersionByte = 0x21

// Address is a Bitcoin-style Base58Check-encoded identity string.
type Address string

// DeriveAddress computes a Base58Check address from an Ed25519 public key.
// Pipeline: pubKey → SHA-256 → RIPEMD-160 → Base58Check(version 0x21).
func DeriveAddress(pubKey ed25519.PublicKey) Address {
	// SHA-256 of the public key.
	sha := sha256.Sum256(pubKey)

	// RIPEMD-160 of the SHA-256 hash.
	rip := ripemd160.New()
	rip.Write(sha[:])
	hash160 := rip.Sum(nil)

	// Prepend version byte.
	versioned := make([]byte, 0, 1+len(hash160)+4)
	versioned = append(versioned, VersionByte)
	versioned = append(versioned, hash160...)

	// Double SHA-256 checksum (first 4 bytes).
	first := sha256.Sum256(versioned)
	second := sha256.Sum256(first[:])
	checksum := second[:4]

	// Final: version + hash160 + checksum.
	full := append(versioned, checksum...)
	return Address(base58.Encode(full))
}

// Validate checks that an address has a valid Base58Check checksum and version byte.
func Validate(addr string) bool {
	data, err := base58.Decode(addr)
	if err != nil {
		return false
	}
	// Minimum: 1 (version) + 20 (hash160) + 4 (checksum) = 25 bytes.
	if len(data) < 25 {
		return false
	}
	if data[0] != VersionByte {
		return false
	}

	payload := data[:len(data)-4]
	checksum := data[len(data)-4:]

	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])

	return second[0] == checksum[0] &&
		second[1] == checksum[1] &&
		second[2] == checksum[2] &&
		second[3] == checksum[3]
}

// MatchesPublicKey checks if an address was derived from the given public key.
func MatchesPublicKey(addr string, pubKey ed25519.PublicKey) bool {
	return Validate(addr) && Address(addr) == DeriveAddress(pubKey)
}

// Short returns a truncated display form like "EaBC12...xYz9".
func (a Address) Short() string {
	s := string(a)
	if len(s) <= 12 {
		return s
	}
	return fmt.Sprintf("%s...%s", s[:6], s[len(s)-4:])
}

// String implements the Stringer interface.
func (a Address) String() string {
	return string(a)
}
