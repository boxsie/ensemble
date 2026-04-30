package identity

import (
	"crypto/ed25519"
	"crypto/sha512"
	"fmt"
	"math/big"
)

// fieldPrime is p = 2^255 - 19 for Curve25519.
var fieldPrime, _ = new(big.Int).SetString(
	"7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffed", 16)

// ToX25519PrivateKey converts the Ed25519 private key to an X25519 private key.
// Per RFC 8032 § 5.1.5: SHA-512(seed), clamp low 32 bytes.
func (k *Keypair) ToX25519PrivateKey() []byte {
	digest := sha512.Sum512(k.priv.Seed())

	out := make([]byte, 32)
	copy(out, digest[:32])

	out[0] &= 248
	out[31] &= 127
	out[31] |= 64

	return out
}

// ToX25519PublicKey converts the Ed25519 public key to an X25519 public key.
// Uses the birational map: u = (1 + y) / (1 - y) mod p.
func (k *Keypair) ToX25519PublicKey() ([]byte, error) {
	return Ed25519PublicKeyToX25519(k.pub)
}

// Ed25519PublicKeyToX25519 converts a raw Ed25519 public key to X25519.
func Ed25519PublicKeyToX25519(pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: got %d, want %d", len(pub), ed25519.PublicKeySize)
	}

	one := big.NewInt(1)

	// Ed25519 encodes y as little-endian with the sign of x in the MSB.
	yBytes := make([]byte, 32)
	copy(yBytes, pub)
	yBytes[31] &= 0x7f // clear sign bit

	// Reverse to big-endian for big.Int.
	yBigBytes := make([]byte, 32)
	for i := range 32 {
		yBigBytes[i] = yBytes[31-i]
	}
	y := new(big.Int).SetBytes(yBigBytes)

	// u = (1 + y) * (1 - y)^{-1} mod p
	num := new(big.Int).Add(one, y)
	den := new(big.Int).Sub(one, y)
	denInv := new(big.Int).ModInverse(den, fieldPrime)
	if denInv == nil {
		return nil, fmt.Errorf("degenerate ed25519 public key")
	}

	u := new(big.Int).Mul(num, denInv)
	u.Mod(u, fieldPrime)

	// big.Int → little-endian 32 bytes.
	uBytes := u.Bytes()
	out := make([]byte, 32)
	for i := range len(uBytes) {
		out[i] = uBytes[len(uBytes)-1-i]
	}

	return out, nil
}
