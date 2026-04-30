package api

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	headerPubKey    = "x-auth-pubkey"
	headerTimestamp = "x-auth-timestamp"
	headerSignature = "x-auth-signature"

	// authFreshness is how old a signed timestamp can be before rejection.
	authFreshness = 5 * time.Minute
)

// AdminKeyUnaryInterceptor returns a gRPC unary interceptor that verifies
// requests are signed by the given admin Ed25519 public key.
func AdminKeyUnaryInterceptor(adminPubKey ed25519.PublicKey) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := verifyAuth(ctx, adminPubKey); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// AdminKeyStreamInterceptor returns a gRPC stream interceptor that verifies
// streams are signed by the given admin Ed25519 public key.
func AdminKeyStreamInterceptor(adminPubKey ed25519.PublicKey) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := verifyAuth(ss.Context(), adminPubKey); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func verifyAuth(ctx context.Context, adminPubKey ed25519.PublicKey) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}

	pubHex := mdGet(md, headerPubKey)
	tsStr := mdGet(md, headerTimestamp)
	sigHex := mdGet(md, headerSignature)

	if pubHex == "" || tsStr == "" || sigHex == "" {
		return status.Error(codes.Unauthenticated, "missing auth headers")
	}

	// Decode public key and verify it matches the admin key.
	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return status.Error(codes.Unauthenticated, "invalid public key")
	}
	if !ed25519.PublicKey(pubBytes).Equal(adminPubKey) {
		return status.Error(codes.PermissionDenied, "unauthorized public key")
	}

	// Check timestamp freshness.
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return status.Error(codes.Unauthenticated, "invalid timestamp")
	}
	age := time.Since(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > authFreshness {
		return status.Error(codes.Unauthenticated, "timestamp too old")
	}

	// Verify signature over the timestamp string.
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return status.Error(codes.Unauthenticated, "invalid signature encoding")
	}
	if !ed25519.Verify(adminPubKey, []byte(tsStr), sig) {
		return status.Error(codes.Unauthenticated, "invalid signature")
	}

	return nil
}

func mdGet(md metadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// SignedCredentials implements grpc.PerRPCCredentials using Ed25519 signing.
// Each RPC includes a signed timestamp proving the caller holds the private key.
type SignedCredentials struct {
	PrivKey ed25519.PrivateKey
}

func (c SignedCredentials) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	pubKey := c.PrivKey.Public().(ed25519.PublicKey)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := ed25519.Sign(c.PrivKey, []byte(ts))

	return map[string]string{
		headerPubKey:    hex.EncodeToString(pubKey),
		headerTimestamp: ts,
		headerSignature: hex.EncodeToString(sig),
	}, nil
}

func (c SignedCredentials) RequireTransportSecurity() bool {
	return false
}
