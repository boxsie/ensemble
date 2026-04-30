package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/boxsie/ensemble/internal/identity"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
)

func TestWriteAndReadMsg(t *testing.T) {
	kp, _ := identity.Generate()
	env := WrapMessage(pb.MessageType_CHAT_TEXT, []byte("hello"), kp)

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatalf("WriteMsg() error: %v", err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatalf("ReadMsg() error: %v", err)
	}

	if got.Type != env.Type {
		t.Fatalf("type: got %v, want %v", got.Type, env.Type)
	}
	if !bytes.Equal(got.Payload, env.Payload) {
		t.Fatal("payload mismatch")
	}
	if got.Timestamp != env.Timestamp {
		t.Fatal("timestamp mismatch")
	}
}

func TestWrapAndVerifyEnvelope(t *testing.T) {
	kp, _ := identity.Generate()
	env := WrapMessage(pb.MessageType_PING, []byte("ping"), kp)

	if !VerifyEnvelope(env) {
		t.Fatal("envelope should verify with correct key")
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	kp, _ := identity.Generate()
	env := WrapMessage(pb.MessageType_PING, []byte("ping"), kp)

	env.Payload = []byte("tampered")
	if VerifyEnvelope(env) {
		t.Fatal("envelope should not verify after payload tampering")
	}
}

func TestVerifyRejectsTamperedTimestamp(t *testing.T) {
	kp, _ := identity.Generate()
	env := WrapMessage(pb.MessageType_PING, []byte("ping"), kp)

	env.Timestamp = env.Timestamp + 1
	if VerifyEnvelope(env) {
		t.Fatal("envelope should not verify after timestamp tampering")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	kp1, _ := identity.Generate()
	kp2, _ := identity.Generate()
	env := WrapMessage(pb.MessageType_PING, []byte("ping"), kp1)

	// Replace the public key with a different one.
	env.PubKey = kp2.PublicKey()
	if VerifyEnvelope(env) {
		t.Fatal("envelope should not verify with wrong public key")
	}
}

func TestReadMsgRejectsOversized(t *testing.T) {
	// Write a length prefix that exceeds MaxMessageSize.
	var buf bytes.Buffer
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(MaxMessageSize+1))
	buf.Write(length)

	_, err := ReadMsg(&buf)
	if err == nil {
		t.Fatal("ReadMsg() should reject oversized messages")
	}
}

func TestWriteMsgRejectsOversized(t *testing.T) {
	env := &pb.Envelope{
		Payload: make([]byte, MaxMessageSize+1),
	}
	var buf bytes.Buffer
	err := WriteMsg(&buf, env)
	if err == nil {
		t.Fatal("WriteMsg() should reject oversized messages")
	}
}

func TestRoundTripMultipleMessages(t *testing.T) {
	kp, _ := identity.Generate()
	var buf bytes.Buffer

	types := []pb.MessageType{
		pb.MessageType_CHAT_TEXT,
		pb.MessageType_PING,
		pb.MessageType_FILE_OFFER,
	}

	for _, mt := range types {
		env := WrapMessage(mt, []byte("data"), kp)
		if err := WriteMsg(&buf, env); err != nil {
			t.Fatalf("WriteMsg() error: %v", err)
		}
	}

	for _, mt := range types {
		env, err := ReadMsg(&buf)
		if err != nil {
			t.Fatalf("ReadMsg() error: %v", err)
		}
		if env.Type != mt {
			t.Fatalf("type: got %v, want %v", env.Type, mt)
		}
		if !VerifyEnvelope(env) {
			t.Fatalf("envelope for type %v should verify", mt)
		}
	}
}
