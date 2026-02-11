package filetransfer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"google.golang.org/protobuf/proto"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/node"
	protoc "github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/transport"
)

// FileOffer represents an incoming file offer for the UI/callback.
type FileOffer struct {
	TransferID string
	Filename   string
	Size       uint64
	RootHash   []byte
	ChunkSize  uint32
	ChunkCount uint32
	FromAddr   string
	Decision   chan FileDecision // send accept/reject
}

// FileDecision is the user's response to a file offer.
type FileDecision struct {
	Accept   bool
	SavePath string // directory to save the file into
	Reason   string // rejection reason
}

// Receiver accepts file offers, receives chunks, verifies, and reassembles files.
type Receiver struct {
	keypair  *identity.Keypair
	host     *transport.Host
	eventBus *node.EventBus
	saveDir  string // default save directory

	onProgress ProgressFunc

	mu        sync.Mutex
	transfers map[string]*Transfer
	pending   map[string]*FileOffer
}

// NewReceiver creates a file transfer receiver and registers the stream handler.
func NewReceiver(kp *identity.Keypair, host *transport.Host, bus *node.EventBus, saveDir string) *Receiver {
	r := &Receiver{
		keypair:   kp,
		host:      host,
		eventBus:  bus,
		saveDir:   saveDir,
		transfers: make(map[string]*Transfer),
		pending:   make(map[string]*FileOffer),
	}
	host.SetStreamHandler(protocol.ID(protoc.ProtocolFileTransfer), r.handleIncoming)
	return r
}

// SetProgressCallback sets a function called on each verified chunk.
func (r *Receiver) SetProgressCallback(fn ProgressFunc) {
	r.onProgress = fn
}

// Close unregisters the stream handler.
func (r *Receiver) Close() {
	r.host.RemoveStreamHandler(protocol.ID(protoc.ProtocolFileTransfer))
}

// AcceptFile accepts a pending file offer.
func (r *Receiver) AcceptFile(transferID, savePath string) error {
	r.mu.Lock()
	offer, ok := r.pending[transferID]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("no pending offer with ID %s", transferID)
	}

	offer.Decision <- FileDecision{Accept: true, SavePath: savePath}
	return nil
}

// RejectFile rejects a pending file offer.
func (r *Receiver) RejectFile(transferID, reason string) error {
	r.mu.Lock()
	offer, ok := r.pending[transferID]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("no pending offer with ID %s", transferID)
	}

	offer.Decision <- FileDecision{Accept: false, Reason: reason}
	return nil
}

// GetTransfer returns a transfer by ID.
func (r *Receiver) GetTransfer(id string) *Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.transfers[id]
}

func (r *Receiver) handleIncoming(stream network.Stream) {
	defer stream.Close()

	// Read the offer
	stream.SetReadDeadline(time.Now().Add(ChunkTimeout))
	env, err := protoc.ReadMsg(stream)
	if err != nil {
		return
	}
	if env.Type != pb.MessageType_FILE_OFFER {
		return
	}
	if !protoc.VerifyEnvelope(env) {
		return
	}

	offer := &pb.FileOffer{}
	if err := proto.Unmarshal(env.Payload, offer); err != nil {
		return
	}

	fromAddr := identity.DeriveAddress(env.PubKey).String()

	// Create pending offer and notify via event bus
	decisionCh := make(chan FileDecision, 1)
	fo := &FileOffer{
		TransferID: offer.TransferId,
		Filename:   offer.Filename,
		Size:       offer.Size,
		RootHash:   offer.RootHash,
		ChunkSize:  offer.ChunkSize,
		ChunkCount: offer.ChunkCount,
		FromAddr:   fromAddr,
		Decision:   decisionCh,
	}

	r.mu.Lock()
	r.pending[offer.TransferId] = fo
	r.mu.Unlock()

	r.eventBus.Publish(node.Event{
		Type:    node.EventFileOffer,
		Payload: fo,
	})

	// Wait for decision (with timeout)
	var decision FileDecision
	select {
	case decision = <-decisionCh:
	case <-time.After(2 * time.Minute):
		decision = FileDecision{Accept: false, Reason: "timed out"}
	}

	r.mu.Lock()
	delete(r.pending, offer.TransferId)
	r.mu.Unlock()

	if !decision.Accept {
		reject := &pb.FileReject{TransferId: offer.TransferId, Reason: decision.Reason}
		payload, _ := proto.Marshal(reject)
		env := protoc.WrapMessage(pb.MessageType_FILE_REJECT, payload, r.keypair)
		stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
		protoc.WriteMsg(stream, env)
		return
	}

	// Send accept
	accept := &pb.FileAccept{TransferId: offer.TransferId}
	payload, _ := proto.Marshal(accept)
	acceptEnv := protoc.WrapMessage(pb.MessageType_FILE_ACCEPT, payload, r.keypair)
	stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
	if err := protoc.WriteMsg(stream, acceptEnv); err != nil {
		return
	}

	// Receive chunks
	saveDir := decision.SavePath
	if saveDir == "" {
		saveDir = r.saveDir
	}

	xfer := &Transfer{
		ID:        offer.TransferId,
		Filename:  offer.Filename,
		FileSize:  int64(offer.Size),
		ChunkSize: int(offer.ChunkSize),
		Total:     offer.ChunkCount,
		RootHash:  offer.RootHash,
		PeerAddr:  fromAddr,
		Direction: "receive",
	}

	r.mu.Lock()
	r.transfers[offer.TransferId] = xfer
	r.mu.Unlock()

	err = r.receiveChunks(stream, xfer, saveDir)
	if err != nil {
		xfer.mu.Lock()
		xfer.failed = true
		xfer.mu.Unlock()
	}
}

func (r *Receiver) receiveChunks(stream network.Stream, xfer *Transfer, saveDir string) error {
	chunks := make(map[uint32][]byte) // index → data

	for {
		stream.SetReadDeadline(time.Now().Add(ChunkTimeout))
		env, err := protoc.ReadMsg(stream)
		if err != nil {
			return fmt.Errorf("reading chunk: %w", err)
		}

		switch env.Type {
		case pb.MessageType_FILE_CHUNK:
			fc := &pb.FileChunk{}
			if err := proto.Unmarshal(env.Payload, fc); err != nil {
				return fmt.Errorf("unmarshaling chunk: %w", err)
			}

			// Verify SHA-256 hash
			h := sha256.Sum256(fc.Data)
			hashValid := string(h[:]) == string(fc.Hash)

			// Verify Merkle proof
			proofValid := hashValid && VerifyProof(fc.Hash, int(fc.Index), fc.Proof, xfer.RootHash)

			valid := hashValid && proofValid

			// Send ACK
			ack := &pb.FileChunkAck{
				TransferId: xfer.ID,
				Index:      fc.Index,
				Valid:      valid,
			}
			ackPayload, _ := proto.Marshal(ack)
			ackEnv := protoc.WrapMessage(pb.MessageType_FILE_CHUNK_ACK, ackPayload, r.keypair)
			stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
			if err := protoc.WriteMsg(stream, ackEnv); err != nil {
				return fmt.Errorf("sending ACK: %w", err)
			}

			if valid {
				chunks[fc.Index] = fc.Data
				xfer.mu.Lock()
				xfer.acked++
				xfer.mu.Unlock()

				if r.onProgress != nil {
					r.onProgress(xfer.ID, uint32(len(chunks)), xfer.Total)
				}
			}

		case pb.MessageType_FILE_COMPLETE:
			// Reassemble file
			return r.reassemble(xfer, chunks, saveDir)

		case pb.MessageType_FILE_CANCEL:
			return fmt.Errorf("transfer cancelled by sender")

		default:
			return fmt.Errorf("unexpected message type: %v", env.Type)
		}
	}
}

func (r *Receiver) reassemble(xfer *Transfer, chunks map[uint32][]byte, saveDir string) error {
	if uint32(len(chunks)) != xfer.Total {
		return fmt.Errorf("incomplete transfer: got %d/%d chunks", len(chunks), xfer.Total)
	}

	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return fmt.Errorf("creating save directory: %w", err)
	}

	outPath := filepath.Join(saveDir, xfer.Filename)
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	for i := uint32(0); i < xfer.Total; i++ {
		data, ok := chunks[i]
		if !ok {
			return fmt.Errorf("missing chunk %d", i)
		}
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("writing chunk %d: %w", i, err)
		}
		h.Write(data)
	}

	return nil
}
