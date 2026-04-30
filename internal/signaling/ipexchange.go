package signaling

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/protobuf/proto"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

// IPInfo holds network addresses for direct connection.
type IPInfo struct {
	Addrs []string // multiaddrs, e.g. /ip4/1.2.3.4/udp/4001/quic-v1
}

var hkdfInfo = []byte("ensemble-ip-exchange-v1")

// EncryptIPInfo encrypts IPInfo using an ECDH-derived AES-256-GCM key.
// Converts Ed25519 keys to X25519, performs ECDH, derives an AES key via
// HKDF-SHA256, and encrypts the serialized IPExchangeMsg.
func EncryptIPInfo(info *IPInfo, kp *identity.Keypair, peerPub ed25519.PublicKey) ([]byte, error) {
	key, err := deriveSharedKey(kp, peerPub)
	if err != nil {
		return nil, err
	}

	msg := &pb.IPExchangeMsg{Addrs: info.Addrs}
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshaling IP info: %w", err)
	}

	return encryptGCM(key, plaintext)
}

// DecryptIPInfo decrypts ciphertext produced by EncryptIPInfo.
func DecryptIPInfo(ciphertext []byte, kp *identity.Keypair, peerPub ed25519.PublicKey) (*IPInfo, error) {
	key, err := deriveSharedKey(kp, peerPub)
	if err != nil {
		return nil, err
	}

	plaintext, err := decryptGCM(key, ciphertext)
	if err != nil {
		return nil, err
	}

	msg := &pb.IPExchangeMsg{}
	if err := proto.Unmarshal(plaintext, msg); err != nil {
		return nil, fmt.Errorf("unmarshaling IP info: %w", err)
	}
	return &IPInfo{Addrs: msg.Addrs}, nil
}

// ExchangeIPs performs a bidirectional encrypted IP exchange over conn.
// Both sides send their encrypted IP info concurrently and decrypt the
// peer's reply. Uses the existing length-prefixed wire protocol.
func ExchangeIPs(ctx context.Context, conn net.Conn, ourInfo *IPInfo, keypair *identity.Keypair, peerPub ed25519.PublicKey) (*IPInfo, error) {
	encrypted, err := EncryptIPInfo(ourInfo, keypair, peerPub)
	if err != nil {
		return nil, fmt.Errorf("encrypting IP info: %w", err)
	}
	env := wrapUnsigned(pb.MessageType_IP_EXCHANGE, encrypted)

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
		defer conn.SetDeadline(time.Time{})
	}

	// Read and write concurrently to avoid deadlock on synchronous
	// connections (e.g. net.Pipe). TCP is full-duplex so this is safe.
	type readResult struct {
		env *pb.Envelope
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		e, err := protocol.ReadMsg(conn)
		ch <- readResult{e, err}
	}()

	if err := protocol.WriteMsg(conn, env); err != nil {
		return nil, fmt.Errorf("sending IP info: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("receiving IP info: %w", r.err)
		}
		if r.env.Type != pb.MessageType_IP_EXCHANGE {
			return nil, fmt.Errorf("unexpected message type: %v", r.env.Type)
		}
		return DecryptIPInfo(r.env.Payload, keypair, peerPub)
	}
}

// --- crypto helpers ---

// deriveSharedKey performs X25519 ECDH + HKDF-SHA256 to produce a 32-byte AES key.
func deriveSharedKey(kp *identity.Keypair, peerPub ed25519.PublicKey) ([]byte, error) {
	xPriv := kp.ToX25519PrivateKey()
	xPub, err := identity.Ed25519PublicKeyToX25519(peerPub)
	if err != nil {
		return nil, fmt.Errorf("converting peer public key: %w", err)
	}

	shared, err := curve25519.X25519(xPriv, xPub)
	if err != nil {
		return nil, fmt.Errorf("X25519 ECDH: %w", err)
	}

	// HKDF-SHA256 with sorted public keys as salt for deterministic derivation.
	salt := sortedConcat(kp.PublicKey(), peerPub)
	r := hkdf.New(sha256.New, shared, salt, hkdfInfo)
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("HKDF: %w", err)
	}
	return key, nil
}

// sortedConcat returns a||b with the lexicographically smaller slice first.
func sortedConcat(a, b []byte) []byte {
	if len(a) > 0 && len(b) > 0 && a[0] > b[0] {
		a, b = b, a
	} else if len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		// Full comparison needed only if first bytes match.
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] > b[i] {
				a, b = b, a
				break
			} else if a[i] < b[i] {
				break
			}
		}
	}
	out := make([]byte, len(a)+len(b))
	copy(out, a)
	copy(out[len(a):], b)
	return out
}

// encryptGCM encrypts plaintext with AES-256-GCM. Returns nonce || ciphertext.
func encryptGCM(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptGCM decrypts ciphertext produced by encryptGCM.
func decryptGCM(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, ciphertext[:ns], ciphertext[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}
	return plaintext, nil
}
