package transport

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	// StreamTimeout is the default deadline for stream operations.
	StreamTimeout = 30 * time.Second
)

// OpenStream opens a libp2p stream to a connected peer using the given protocol.
func (h *Host) OpenStream(ctx context.Context, peerID peer.ID, proto protocol.ID) (network.Stream, error) {
	s, err := h.host.NewStream(ctx, peerID, proto)
	if err != nil {
		return nil, fmt.Errorf("opening stream to %s: %w", peerID.ShortString(), err)
	}
	return s, nil
}

// SetStreamHandler registers a handler for incoming streams of the given protocol.
func (h *Host) SetStreamHandler(proto protocol.ID, handler network.StreamHandler) {
	h.host.SetStreamHandler(proto, handler)
}

// RemoveStreamHandler removes the handler for the given protocol.
func (h *Host) RemoveStreamHandler(proto protocol.ID) {
	h.host.RemoveStreamHandler(proto)
}

// SendBytes opens a stream, writes data, and closes the write side.
func (h *Host) SendBytes(ctx context.Context, peerID peer.ID, proto protocol.ID, data []byte) error {
	s, err := h.OpenStream(ctx, peerID, proto)
	if err != nil {
		return err
	}
	defer s.Close()

	s.SetWriteDeadline(time.Now().Add(StreamTimeout))
	if _, err := s.Write(data); err != nil {
		s.Reset()
		return fmt.Errorf("writing to stream: %w", err)
	}
	return s.CloseWrite()
}

// ReadAll reads the full contents from a stream up to maxBytes.
func ReadAll(s network.Stream, maxBytes int) ([]byte, error) {
	s.SetReadDeadline(time.Now().Add(StreamTimeout))
	return io.ReadAll(io.LimitReader(s, int64(maxBytes)))
}
