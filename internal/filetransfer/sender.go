package filetransfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"google.golang.org/protobuf/proto"

	"github.com/boxsie/ensemble/internal/identity"
	protoc "github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/transport"
)

const (
	WindowSize    = 8 // max chunks in-flight
	MaxRetries    = 3 // per-chunk retransmit limit
	ChunkTimeout  = 30 * time.Second
)

// Transfer tracks the state of a file transfer.
type Transfer struct {
	ID        string
	Filename  string
	FileSize  int64
	ChunkSize int
	Total     uint32
	RootHash  []byte
	PeerAddr  string
	Direction string // "send" or "receive"

	mu       sync.Mutex
	sent     uint32
	acked    uint32
	failed   bool
	cancelFn context.CancelFunc
}

// Acked returns the number of acknowledged chunks.
func (t *Transfer) Acked() uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.acked
}

// Failed returns whether the transfer has failed.
func (t *Transfer) Failed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.failed
}

// PeerResolver maps ensemble addresses to connected peer info.
type PeerResolver interface {
	GetPeer(addr string) *transport.PeerConnection
}

// ProgressFunc is called with progress updates during transfer.
type ProgressFunc func(transferID string, acked, total uint32)

// Sender offers files and sends chunks with flow control.
type Sender struct {
	keypair  *identity.Keypair
	host     *transport.Host
	resolver PeerResolver
	onProgress ProgressFunc

	mu        sync.Mutex
	transfers map[string]*Transfer
}

// NewSender creates a file transfer sender.
func NewSender(kp *identity.Keypair, host *transport.Host, resolver PeerResolver) *Sender {
	return &Sender{
		keypair:   kp,
		host:      host,
		resolver:  resolver,
		transfers: make(map[string]*Transfer),
	}
}

// SetProgressCallback sets a function called on each chunk ACK.
func (s *Sender) SetProgressCallback(fn ProgressFunc) {
	s.onProgress = fn
}

// OfferFile chunks the file, builds a Merkle tree, sends a FileOffer, and
// on acceptance sends all chunks with a sliding window.
func (s *Sender) OfferFile(ctx context.Context, peerAddr, filePath string) (*Transfer, error) {
	pc := s.resolver.GetPeer(peerAddr)
	if pc == nil {
		return nil, fmt.Errorf("peer %s not connected", peerAddr)
	}

	chunker, err := NewChunker(filePath)
	if err != nil {
		return nil, fmt.Errorf("creating chunker: %w", err)
	}

	hashes, err := chunker.AllChunkHashes()
	if err != nil {
		return nil, fmt.Errorf("computing chunk hashes: %w", err)
	}

	tree, err := BuildMerkleTree(hashes)
	if err != nil {
		return nil, fmt.Errorf("building merkle tree: %w", err)
	}

	transferID := generateTransferID()
	ctx, cancel := context.WithCancel(ctx)

	xfer := &Transfer{
		ID:        transferID,
		Filename:  filepath.Base(filePath),
		FileSize:  chunker.FileSize(),
		ChunkSize: chunker.ChunkSize(),
		Total:     chunker.TotalChunks(),
		RootHash:  tree.Root(),
		PeerAddr:  peerAddr,
		Direction: "send",
		cancelFn:  cancel,
	}

	s.mu.Lock()
	s.transfers[transferID] = xfer
	s.mu.Unlock()

	// Open stream to peer
	stream, err := s.host.OpenStream(ctx, pc.PeerID, protocol.ID(protoc.ProtocolFileTransfer))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening stream: %w", err)
	}

	// Send FileOffer
	offer := &pb.FileOffer{
		TransferId: transferID,
		Filename:   xfer.Filename,
		Size:       uint64(xfer.FileSize),
		RootHash:   tree.Root(),
		ChunkSize:  uint32(xfer.ChunkSize),
		ChunkCount: xfer.Total,
	}
	offerPayload, err := proto.Marshal(offer)
	if err != nil {
		stream.Close()
		cancel()
		return nil, fmt.Errorf("marshaling offer: %w", err)
	}

	env := protoc.WrapMessage(pb.MessageType_FILE_OFFER, offerPayload, s.keypair)
	stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
	if err := protoc.WriteMsg(stream, env); err != nil {
		stream.Reset()
		cancel()
		return nil, fmt.Errorf("sending offer: %w", err)
	}

	// Wait for accept/reject with context cancellation support
	type readResult struct {
		env *pb.Envelope
		err error
	}
	readCh := make(chan readResult, 1)
	go func() {
		stream.SetReadDeadline(time.Now().Add(2 * time.Minute))
		env, err := protoc.ReadMsg(stream)
		readCh <- readResult{env, err}
	}()

	var respEnv *pb.Envelope
	select {
	case <-ctx.Done():
		stream.Reset()
		cancel()
		return nil, ctx.Err()
	case res := <-readCh:
		if res.err != nil {
			stream.Reset()
			cancel()
			return nil, fmt.Errorf("reading response: %w", res.err)
		}
		respEnv = res.env
	}

	if respEnv.Type == pb.MessageType_FILE_REJECT {
		stream.Close()
		cancel()
		return nil, fmt.Errorf("file rejected by peer")
	}
	if respEnv.Type != pb.MessageType_FILE_ACCEPT {
		stream.Reset()
		cancel()
		return nil, fmt.Errorf("unexpected response type: %v", respEnv.Type)
	}

	// Send chunks with sliding window
	err = s.sendChunks(ctx, stream, pc.PeerID, chunker, tree, xfer)
	if err != nil {
		stream.Reset()
		xfer.mu.Lock()
		xfer.failed = true
		xfer.mu.Unlock()
		cancel()
		return xfer, err
	}

	// Close write side and wait for receiver to finish (read until EOF)
	stream.CloseWrite()
	stream.SetReadDeadline(time.Now().Add(ChunkTimeout))
	protoc.ReadMsg(stream) // will return error (EOF) when receiver closes — expected

	stream.Close()
	cancel()
	return xfer, nil
}

// sendChunks transmits all chunks using a sliding window with ACK-driven flow control.
func (s *Sender) sendChunks(ctx context.Context, stream interface {
	SetWriteDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	Read([]byte) (int, error)
	Write([]byte) (int, error)
}, _ peer.ID, chunker *Chunker, tree *MerkleTree, xfer *Transfer) error {

	retries := make(map[uint32]int) // chunk index → retry count
	nextToSend := uint32(0)
	inFlight := uint32(0)
	acked := make(map[uint32]bool)
	total := chunker.TotalChunks()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Send chunks up to window size
		for inFlight < WindowSize && nextToSend < total {
			if acked[nextToSend] {
				nextToSend++
				continue
			}

			if err := s.sendOneChunk(stream, chunker, tree, xfer.ID, nextToSend); err != nil {
				return fmt.Errorf("sending chunk %d: %w", nextToSend, err)
			}

			inFlight++
			nextToSend++
		}

		if len(acked) >= int(total) {
			// All chunks acknowledged — send FileComplete
			complete := &pb.FileComplete{TransferId: xfer.ID}
			payload, _ := proto.Marshal(complete)
			env := protoc.WrapMessage(pb.MessageType_FILE_COMPLETE, payload, s.keypair)
			stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
			if err := protoc.WriteMsg(stream, env); err != nil {
				return fmt.Errorf("sending complete: %w", err)
			}
			return nil
		}

		// Read one ACK
		stream.SetReadDeadline(time.Now().Add(ChunkTimeout))
		ackEnv, err := protoc.ReadMsg(stream)
		if err != nil {
			return fmt.Errorf("reading ACK: %w", err)
		}

		if ackEnv.Type == pb.MessageType_FILE_CANCEL {
			return fmt.Errorf("transfer cancelled by peer")
		}

		if ackEnv.Type != pb.MessageType_FILE_CHUNK_ACK {
			return fmt.Errorf("unexpected message type: %v", ackEnv.Type)
		}

		chunkAck := &pb.FileChunkAck{}
		if err := proto.Unmarshal(ackEnv.Payload, chunkAck); err != nil {
			return fmt.Errorf("unmarshaling ACK: %w", err)
		}

		inFlight--

		if chunkAck.Valid {
			acked[chunkAck.Index] = true
			xfer.mu.Lock()
			xfer.acked++
			xfer.mu.Unlock()

			if s.onProgress != nil {
				s.onProgress(xfer.ID, uint32(len(acked)), total)
			}
		} else {
			// Chunk failed verification — retransmit
			retries[chunkAck.Index]++
			if retries[chunkAck.Index] > MaxRetries {
				return fmt.Errorf("chunk %d failed verification after %d retries", chunkAck.Index, MaxRetries)
			}

			if err := s.sendOneChunk(stream, chunker, tree, xfer.ID, chunkAck.Index); err != nil {
				return fmt.Errorf("retransmitting chunk %d: %w", chunkAck.Index, err)
			}
			inFlight++
		}
	}
}

func (s *Sender) sendOneChunk(stream interface {
	SetWriteDeadline(time.Time) error
	Write([]byte) (int, error)
}, chunker *Chunker, tree *MerkleTree, transferID string, index uint32) error {

	chunk, err := chunker.ReadChunk(index)
	if err != nil {
		return fmt.Errorf("reading chunk: %w", err)
	}

	proof, err := tree.Proof(int(index))
	if err != nil {
		return fmt.Errorf("generating proof: %w", err)
	}

	fc := &pb.FileChunk{
		TransferId: transferID,
		Index:      index,
		Data:       chunk.Data,
		Hash:       chunk.Hash,
		Proof:      proof,
	}
	payload, err := proto.Marshal(fc)
	if err != nil {
		return fmt.Errorf("marshaling chunk: %w", err)
	}

	env := protoc.WrapMessage(pb.MessageType_FILE_CHUNK, payload, s.keypair)
	stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
	return protoc.WriteMsg(stream, env)
}

// CancelTransfer cancels an in-progress transfer.
func (s *Sender) CancelTransfer(id string) {
	s.mu.Lock()
	xfer, ok := s.transfers[id]
	s.mu.Unlock()

	if ok && xfer.cancelFn != nil {
		xfer.cancelFn()
	}
}

// GetTransfer returns a transfer by ID.
func (s *Sender) GetTransfer(id string) *Transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfers[id]
}

func generateTransferID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
