package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func genTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return pub, priv
}

func signedCtx(t *testing.T, priv ed25519.PrivateKey) context.Context {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(priv, []byte(ts))
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		headerPubKey, hex.EncodeToString(pub),
		headerTimestamp, ts,
		headerSignature, hex.EncodeToString(sig),
	))
}

func TestVerifyAuth_Valid(t *testing.T) {
	pub, priv := genTestKey(t)
	ctx := signedCtx(t, priv)
	if err := verifyAuth(ctx, pub); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestVerifyAuth_WrongKey(t *testing.T) {
	_, priv := genTestKey(t)
	otherPub, _ := genTestKey(t)
	ctx := signedCtx(t, priv)
	err := verifyAuth(ctx, otherPub)
	if err == nil {
		t.Fatal("expected error for wrong key")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestVerifyAuth_MissingHeaders(t *testing.T) {
	pub, _ := genTestKey(t)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs())
	err := verifyAuth(ctx, pub)
	if err == nil {
		t.Fatal("expected error for missing headers")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestVerifyAuth_NoMetadata(t *testing.T) {
	pub, _ := genTestKey(t)
	err := verifyAuth(context.Background(), pub)
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
}

func TestVerifyAuth_StaleTimestamp(t *testing.T) {
	pub, priv := genTestKey(t)
	staleTs := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	sig := ed25519.Sign(priv, []byte(staleTs))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		headerPubKey, hex.EncodeToString(pub),
		headerTimestamp, staleTs,
		headerSignature, hex.EncodeToString(sig),
	))
	err := verifyAuth(ctx, pub)
	if err == nil {
		t.Fatal("expected error for stale timestamp")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestVerifyAuth_BadSignature(t *testing.T) {
	pub, _ := genTestKey(t)
	_, otherPriv := genTestKey(t)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(otherPriv, []byte(ts)) // signed by wrong key
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		headerPubKey, hex.EncodeToString(pub),
		headerTimestamp, ts,
		headerSignature, hex.EncodeToString(sig),
	))
	err := verifyAuth(ctx, pub)
	if err == nil {
		t.Fatal("expected error for bad signature")
	}
}

func TestSignedCredentials_GetRequestMetadata(t *testing.T) {
	_, priv := genTestKey(t)
	creds := SignedCredentials{PrivKey: priv}
	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md[headerPubKey] == "" {
		t.Fatal("expected pubkey in metadata")
	}
	if md[headerTimestamp] == "" {
		t.Fatal("expected timestamp in metadata")
	}
	if md[headerSignature] == "" {
		t.Fatal("expected signature in metadata")
	}
}

func TestSignedCredentials_RequireTransportSecurity(t *testing.T) {
	creds := SignedCredentials{}
	if creds.RequireTransportSecurity() {
		t.Fatal("should not require transport security")
	}
}
