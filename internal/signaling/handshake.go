package signaling

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
)

const (
	// NonceSize is the byte length of handshake nonces.
	NonceSize = 32

	// TimestampFreshness is the maximum age of a ConnRequest timestamp.
	TimestampFreshness = 5 * time.Minute

	// NonceCacheTTL is how long replayed nonces are remembered.
	NonceCacheTTL = 10 * time.Minute
)

// NonceCache provides replay protection by remembering recently used nonces.
type NonceCache struct {
	mu      sync.Mutex
	entries map[[NonceSize]byte]time.Time
}

// NewNonceCache creates a new nonce cache.
func NewNonceCache() *NonceCache {
	return &NonceCache{
		entries: make(map[[NonceSize]byte]time.Time),
	}
}

// Check returns true if the nonce has NOT been seen before (i.e., it's fresh).
// If fresh, the nonce is recorded. Returns false on replay.
func (nc *NonceCache) Check(nonce []byte) bool {
	if len(nonce) != NonceSize {
		return false
	}
	var key [NonceSize]byte
	copy(key[:], nonce)

	nc.mu.Lock()
	defer nc.mu.Unlock()

	// Prune expired entries.
	now := time.Now()
	for k, t := range nc.entries {
		if now.Sub(t) > NonceCacheTTL {
			delete(nc.entries, k)
		}
	}

	if _, seen := nc.entries[key]; seen {
		return false
	}
	nc.entries[key] = now
	return true
}

// GenerateNonce creates a cryptographically random nonce.
func GenerateNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	return nonce, nil
}

// CreateConnRequest builds a ConnRequest message (step 1).
// Only addresses (hashes) are included — no public keys.
func CreateConnRequest(fromAddr, targetAddr string) ([]byte, []byte, error) {
	nonce, err := GenerateNonce()
	if err != nil {
		return nil, nil, err
	}

	msg := &pb.ConnRequest{
		FromAddress:   fromAddr,
		TargetAddress: targetAddr,
		Nonce:         nonce,
		Timestamp:     time.Now().UnixMilli(),
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, nil, fmt.Errorf("marshalling ConnRequest: %w", err)
	}
	return data, nonce, nil
}

// ValidateConnRequest checks a ConnRequest for freshness and valid nonce.
func ValidateConnRequest(data []byte, nc *NonceCache) (*pb.ConnRequest, error) {
	var msg pb.ConnRequest
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshalling ConnRequest: %w", err)
	}

	// Check timestamp freshness.
	ts := time.UnixMilli(msg.Timestamp)
	if time.Since(ts) > TimestampFreshness {
		return nil, fmt.Errorf("request too old: %v", time.Since(ts))
	}
	if ts.After(time.Now().Add(TimestampFreshness)) {
		return nil, fmt.Errorf("request timestamp in the future")
	}

	// Check nonce length.
	if len(msg.Nonce) != NonceSize {
		return nil, fmt.Errorf("invalid nonce length: %d", len(msg.Nonce))
	}

	// Replay protection.
	if !nc.Check(msg.Nonce) {
		return nil, fmt.Errorf("replayed nonce")
	}

	return &msg, nil
}

// CreateConnResponse builds a ConnResponse message (step 2).
// If accepted, includes the responder's public key and signs the request nonce.
func CreateConnResponse(accepted bool, kp *identity.Keypair, requestNonce []byte, reason string) ([]byte, []byte, error) {
	msg := &pb.ConnResponse{
		Accepted: accepted,
	}

	var respNonce []byte
	if accepted {
		msg.PubKey = kp.PublicKey()
		msg.Signature = kp.Sign(requestNonce)

		var err error
		respNonce, err = GenerateNonce()
		if err != nil {
			return nil, nil, err
		}
		msg.Nonce = respNonce
	} else {
		msg.Reason = reason
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, nil, fmt.Errorf("marshalling ConnResponse: %w", err)
	}
	return data, respNonce, nil
}

// ValidateConnResponse verifies a ConnResponse (step 2).
// Checks that the responder's public key hashes to targetAddr and the signature is valid.
func ValidateConnResponse(data []byte, requestNonce []byte, targetAddr string) (*pb.ConnResponse, error) {
	var msg pb.ConnResponse
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshalling ConnResponse: %w", err)
	}

	if !msg.Accepted {
		return &msg, nil // rejection — no key material to verify
	}

	// Verify public key hashes to the target address.
	if !identity.MatchesPublicKey(targetAddr, ed25519.PublicKey(msg.PubKey)) {
		return nil, fmt.Errorf("public key does not match target address")
	}

	// Verify signature over the request nonce.
	verifier, err := identity.FromPublicKey(msg.PubKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	if !verifier.Verify(requestNonce, msg.Signature) {
		return nil, fmt.Errorf("invalid signature on request nonce")
	}

	// Verify responder nonce is present.
	if len(msg.Nonce) != NonceSize {
		return nil, fmt.Errorf("invalid responder nonce length: %d", len(msg.Nonce))
	}

	return &msg, nil
}

// CreateConnConfirm builds a ConnConfirm message (step 3).
// Initiator proves identity by signing the responder's nonce.
func CreateConnConfirm(kp *identity.Keypair, responderNonce []byte) ([]byte, error) {
	msg := &pb.ConnConfirm{
		PubKey:    kp.PublicKey(),
		Signature: kp.Sign(responderNonce),
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshalling ConnConfirm: %w", err)
	}
	return data, nil
}

// ValidateConnConfirm verifies a ConnConfirm (step 3).
// Checks that the initiator's public key hashes to fromAddr and the signature is valid.
func ValidateConnConfirm(data []byte, responderNonce []byte, fromAddr string) (*pb.ConnConfirm, error) {
	var msg pb.ConnConfirm
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshalling ConnConfirm: %w", err)
	}

	// Verify public key hashes to the claimed address.
	if !identity.MatchesPublicKey(fromAddr, ed25519.PublicKey(msg.PubKey)) {
		return nil, fmt.Errorf("public key does not match from address")
	}

	// Verify signature over the responder nonce.
	verifier, err := identity.FromPublicKey(msg.PubKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	if !verifier.Verify(responderNonce, msg.Signature) {
		return nil, fmt.Errorf("invalid signature on responder nonce")
	}

	return &msg, nil
}
