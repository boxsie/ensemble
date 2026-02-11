package signaling

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

const (
	// HandshakeTimeout is the deadline for each individual handshake attempt.
	HandshakeTimeout = 30 * time.Second

	// MaxRetries is the number of times to retry on transient errors.
	MaxRetries = 3

	// RetryBackoff is the base delay between retries (doubles each attempt).
	RetryBackoff = 2 * time.Second
)

// ErrRejected is returned when the peer explicitly rejects the connection.
// This is not retried.
var ErrRejected = errors.New("connection rejected")

// HandshakeResult contains the outcome of a successful handshake.
type HandshakeResult struct {
	PeerPubKey ed25519.PublicKey
	PeerAddr   string
}

// Client sends outbound signaling requests to a peer's .onion address.
type Client struct {
	keypair *identity.Keypair
	address string // our ensemble address
	dialer  Dialer
}

// Dialer abstracts network dialing for signaling connections.
type Dialer interface {
	DialContext(ctx context.Context, address string) (net.Conn, error)
}

// NewClient creates a signaling client.
func NewClient(kp *identity.Keypair, addr string, dialer Dialer) *Client {
	return &Client{
		keypair: kp,
		address: addr,
		dialer:  dialer,
	}
}

// RequestConnection performs the full 3-step handshake with a peer.
// Retries on transient errors (timeouts, connection failures) but not on explicit rejections.
func (c *Client) RequestConnection(ctx context.Context, peerOnion string, targetAddr string) (*HandshakeResult, error) {
	var lastErr error

	for attempt := range MaxRetries {
		result, err := c.attemptHandshake(ctx, peerOnion, targetAddr)
		if err == nil {
			return result, nil
		}

		// Don't retry explicit rejections.
		if errors.Is(err, ErrRejected) {
			return nil, err
		}

		lastErr = err

		// Don't retry if context is done.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("after %d attempts: %w", attempt+1, lastErr)
		}

		// Exponential backoff.
		backoff := RetryBackoff * time.Duration(1<<uint(attempt))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("after %d attempts: %w", attempt+1, lastErr)
		}
	}

	return nil, fmt.Errorf("all %d attempts failed: %w", MaxRetries, lastErr)
}

// attemptHandshake performs a single handshake attempt with deadlines.
func (c *Client) attemptHandshake(ctx context.Context, peerOnion string, targetAddr string) (*HandshakeResult, error) {
	// Derive a timeout context for this attempt.
	attemptCtx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()

	conn, err := c.dialer.DialContext(attemptCtx, peerOnion)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", peerOnion, err)
	}
	defer conn.Close()

	// Set a deadline on the connection so reads/writes don't block forever.
	deadline, ok := attemptCtx.Deadline()
	if ok {
		conn.SetDeadline(deadline)
	}

	// Step 1: Send ConnRequest (addresses only, no public keys).
	reqData, reqNonce, err := CreateConnRequest(c.address, targetAddr)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	reqEnv := wrapUnsigned(pb.MessageType_CONN_REQUEST, reqData)
	if err := protocol.WriteMsg(conn, reqEnv); err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	// Step 2: Read ConnResponse (may block while peer prompts user).
	respEnv, err := protocol.ReadMsg(conn)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if respEnv.Type != pb.MessageType_CONN_RESPONSE {
		return nil, fmt.Errorf("unexpected message type: %v", respEnv.Type)
	}

	resp, err := ValidateConnResponse(respEnv.Payload, reqNonce, targetAddr)
	if err != nil {
		return nil, fmt.Errorf("validating response: %w", err)
	}

	if !resp.Accepted {
		reason := resp.Reason
		if reason == "" {
			reason = "rejected"
		}
		return nil, fmt.Errorf("%w: %s", ErrRejected, reason)
	}

	// Step 3: Send ConnConfirm (prove our identity).
	confirmData, err := CreateConnConfirm(c.keypair, resp.Nonce)
	if err != nil {
		return nil, fmt.Errorf("creating confirm: %w", err)
	}
	confirmEnv := wrapUnsigned(pb.MessageType_CONN_CONFIRM, confirmData)
	if err := protocol.WriteMsg(conn, confirmEnv); err != nil {
		return nil, fmt.Errorf("sending confirm: %w", err)
	}

	return &HandshakeResult{
		PeerPubKey: ed25519.PublicKey(resp.PubKey),
		PeerAddr:   targetAddr,
	}, nil
}
