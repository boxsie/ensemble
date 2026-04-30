package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/boxsie/ensemble/internal/identity"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"google.golang.org/protobuf/proto"
)

// WriteMsg writes a length-prefixed protobuf Envelope to w.
func WriteMsg(w io.Writer, env *pb.Envelope) error {
	data, err := proto.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshalling envelope: %w", err)
	}
	if len(data) > MaxMessageSize {
		return fmt.Errorf("message too large: %d bytes (max %d)", len(data), MaxMessageSize)
	}

	// 4-byte big-endian length prefix.
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(data)))

	if _, err := w.Write(length); err != nil {
		return fmt.Errorf("writing length prefix: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("writing payload: %w", err)
	}
	return nil
}

// ReadMsg reads a length-prefixed protobuf Envelope from r.
func ReadMsg(r io.Reader) (*pb.Envelope, error) {
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lengthBuf); err != nil {
		return nil, fmt.Errorf("reading length prefix: %w", err)
	}

	size := binary.BigEndian.Uint32(lengthBuf)
	if size > MaxMessageSize {
		return nil, fmt.Errorf("message too large: %d bytes (max %d)", size, MaxMessageSize)
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("reading payload: %w", err)
	}

	env := &pb.Envelope{}
	if err := proto.Unmarshal(data, env); err != nil {
		return nil, fmt.Errorf("unmarshalling envelope: %w", err)
	}
	return env, nil
}

// signingPayload builds the bytes that get signed: type (4 bytes big-endian) || payload || timestamp (8 bytes big-endian).
func signingPayload(msgType pb.MessageType, payload []byte, timestamp int64) []byte {
	buf := make([]byte, 4+len(payload)+8)
	binary.BigEndian.PutUint32(buf[0:4], uint32(msgType))
	copy(buf[4:4+len(payload)], payload)
	binary.BigEndian.PutUint64(buf[4+len(payload):], uint64(timestamp))
	return buf
}

// WrapMessage creates a signed Envelope from a message type, payload, and keypair.
func WrapMessage(msgType pb.MessageType, payload []byte, kp *identity.Keypair) *pb.Envelope {
	ts := time.Now().UnixMilli()
	data := signingPayload(msgType, payload, ts)
	sig := kp.Sign(data)

	return &pb.Envelope{
		Type:      msgType,
		Payload:   payload,
		Timestamp: ts,
		PubKey:    kp.PublicKey(),
		Signature: sig,
	}
}

// VerifyEnvelope checks the signature on an envelope.
func VerifyEnvelope(env *pb.Envelope) bool {
	kp, err := identity.FromPublicKey(env.PubKey)
	if err != nil {
		return false
	}
	data := signingPayload(env.Type, env.Payload, env.Timestamp)
	return kp.Verify(data, env.Signature)
}
