package signaling

import (
	"testing"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
)

func makeTestPeers(t *testing.T) (alice *identity.Keypair, aliceAddr string, bob *identity.Keypair, bobAddr string) {
	t.Helper()
	var err error
	alice, err = identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	bob, err = identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	aliceAddr = identity.DeriveAddress(alice.PublicKey()).String()
	bobAddr = identity.DeriveAddress(bob.PublicKey()).String()
	return
}

func TestFullHandshake(t *testing.T) {
	alice, aliceAddr, bob, bobAddr := makeTestPeers(t)
	nc := NewNonceCache()

	// Step 1: Alice creates ConnRequest.
	reqData, reqNonce, err := CreateConnRequest(aliceAddr, bobAddr)
	if err != nil {
		t.Fatalf("CreateConnRequest: %v", err)
	}

	// Bob validates the request.
	req, err := ValidateConnRequest(reqData, nc)
	if err != nil {
		t.Fatalf("ValidateConnRequest: %v", err)
	}
	if req.FromAddress != aliceAddr {
		t.Fatalf("wrong from address: %s", req.FromAddress)
	}
	if req.TargetAddress != bobAddr {
		t.Fatalf("wrong target address: %s", req.TargetAddress)
	}

	// Step 2: Bob creates ConnResponse (accepted).
	respData, respNonce, err := CreateConnResponse(true, bob, reqNonce, "")
	if err != nil {
		t.Fatalf("CreateConnResponse: %v", err)
	}

	// Alice validates the response.
	resp, err := ValidateConnResponse(respData, reqNonce, bobAddr)
	if err != nil {
		t.Fatalf("ValidateConnResponse: %v", err)
	}
	if !resp.Accepted {
		t.Fatal("expected accepted")
	}

	// Step 3: Alice creates ConnConfirm.
	confirmData, err := CreateConnConfirm(alice, respNonce)
	if err != nil {
		t.Fatalf("CreateConnConfirm: %v", err)
	}

	// Bob validates the confirm.
	confirm, err := ValidateConnConfirm(confirmData, respNonce, aliceAddr)
	if err != nil {
		t.Fatalf("ValidateConnConfirm: %v", err)
	}

	// Both sides now have each other's public keys.
	if !identity.MatchesPublicKey(aliceAddr, confirm.PubKey) {
		t.Fatal("confirm pub key doesn't match alice's address")
	}
	if !identity.MatchesPublicKey(bobAddr, resp.PubKey) {
		t.Fatal("response pub key doesn't match bob's address")
	}
}

func TestConnRequest_ReplayRejected(t *testing.T) {
	_, aliceAddr, _, bobAddr := makeTestPeers(t)
	nc := NewNonceCache()

	reqData, _, err := CreateConnRequest(aliceAddr, bobAddr)
	if err != nil {
		t.Fatal(err)
	}

	// First validation succeeds.
	if _, err := ValidateConnRequest(reqData, nc); err != nil {
		t.Fatalf("first validation should succeed: %v", err)
	}

	// Replay — same nonce should be rejected.
	if _, err := ValidateConnRequest(reqData, nc); err == nil {
		t.Fatal("replay should be rejected")
	}
}

func TestConnRequest_StaleTimestamp(t *testing.T) {
	_, aliceAddr, _, bobAddr := makeTestPeers(t)
	nc := NewNonceCache()

	// Create a request with an old timestamp.
	nonce, _ := GenerateNonce()
	msg := &pb.ConnRequest{
		FromAddress:   aliceAddr,
		TargetAddress: bobAddr,
		Nonce:         nonce,
		Timestamp:     time.Now().Add(-10 * time.Minute).UnixMilli(),
	}
	data, _ := proto.Marshal(msg)

	_, err := ValidateConnRequest(data, nc)
	if err == nil {
		t.Fatal("stale timestamp should be rejected")
	}
}

func TestConnResponse_Rejected_NoKeyReveal(t *testing.T) {
	_, _, bob, _ := makeTestPeers(t)
	_ = bob

	// Bob rejects — no public key should be included.
	respData, _, err := CreateConnResponse(false, nil, nil, "not accepting strangers")
	if err != nil {
		t.Fatal(err)
	}

	var resp pb.ConnResponse
	proto.Unmarshal(respData, &resp)

	if resp.Accepted {
		t.Fatal("should not be accepted")
	}
	if len(resp.PubKey) > 0 {
		t.Fatal("rejected response should not contain public key")
	}
	if len(resp.Signature) > 0 {
		t.Fatal("rejected response should not contain signature")
	}
	if resp.Reason != "not accepting strangers" {
		t.Fatalf("wrong reason: %s", resp.Reason)
	}
}

func TestConnResponse_WrongKey(t *testing.T) {
	_, aliceAddr, _, bobAddr := makeTestPeers(t)
	nc := NewNonceCache()

	// Alice sends request to Bob.
	reqData, reqNonce, _ := CreateConnRequest(aliceAddr, bobAddr)
	ValidateConnRequest(reqData, nc)

	// An imposter responds with a different key.
	imposter, _ := identity.Generate()
	respData, _, _ := CreateConnResponse(true, imposter, reqNonce, "")

	// Alice validates — should fail because key doesn't hash to Bob's address.
	_, err := ValidateConnResponse(respData, reqNonce, bobAddr)
	if err == nil {
		t.Fatal("imposter's key should not match Bob's address")
	}
}

func TestConnResponse_TamperedSignature(t *testing.T) {
	_, aliceAddr, bob, bobAddr := makeTestPeers(t)
	nc := NewNonceCache()

	reqData, reqNonce, _ := CreateConnRequest(aliceAddr, bobAddr)
	ValidateConnRequest(reqData, nc)

	respData, _, _ := CreateConnResponse(true, bob, reqNonce, "")

	// Tamper with the response.
	var resp pb.ConnResponse
	proto.Unmarshal(respData, &resp)
	resp.Signature[0] ^= 0xFF
	tampered, _ := proto.Marshal(&resp)

	_, err := ValidateConnResponse(tampered, reqNonce, bobAddr)
	if err == nil {
		t.Fatal("tampered signature should be rejected")
	}
}

func TestConnConfirm_WrongKey(t *testing.T) {
	_, _, _, bobAddr := makeTestPeers(t)

	// An imposter creates a confirm claiming to be someone else.
	imposter, _ := identity.Generate()
	imposterAddr := identity.DeriveAddress(imposter.PublicKey()).String()

	nonce, _ := GenerateNonce()
	confirmData, _ := CreateConnConfirm(imposter, nonce)

	// Validate against a different address — should fail.
	_, err := ValidateConnConfirm(confirmData, nonce, bobAddr)
	if err == nil {
		t.Fatal("wrong address should be rejected")
	}

	// Validate against the correct address — should pass.
	_, err = ValidateConnConfirm(confirmData, nonce, imposterAddr)
	if err != nil {
		t.Fatalf("correct address should pass: %v", err)
	}
}

func TestNonceCache_Expiry(t *testing.T) {
	nc := &NonceCache{
		entries: make(map[[NonceSize]byte]time.Time),
	}

	nonce, _ := GenerateNonce()

	// Insert with an old timestamp.
	var key [NonceSize]byte
	copy(key[:], nonce)
	nc.entries[key] = time.Now().Add(-NonceCacheTTL - time.Second)

	// Should be pruned and treated as fresh.
	if !nc.Check(nonce) {
		t.Fatal("expired nonce should be pruned and accepted")
	}
}

func TestNonceCache_InvalidLength(t *testing.T) {
	nc := NewNonceCache()
	if nc.Check([]byte{1, 2, 3}) {
		t.Fatal("short nonce should be rejected")
	}
}
