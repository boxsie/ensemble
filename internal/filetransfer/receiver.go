package filetransfer

import (
	"context"
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
	"github.com/boxsie/ensemble/internal/services"
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

// Receiver accepts file offers, receives chunks, verifies, and reassembles
// files. The keypair used to sign ACK envelopes is resolved through the
// services registry at dispatch time. Receiver implements
// services.FileHandler so it can be installed as the file handler for the
// "node" service in the daemon's service registry.
type Receiver struct {
	host        *transport.Host
	eventBus    *node.EventBus
	saveDir     string // default save directory
	lookup      ServiceLookup
	serviceName string

	onProgress ProgressFunc

	mu        sync.Mutex
	transfers map[string]*Transfer
	pending   map[string]*FileOffer
}

// NewReceiver creates a file transfer receiver and registers the libp2p
// stream handler. The lookup resolves the service whose identity signs ACK
// envelopes; serviceName is the registry key.
func NewReceiver(host *transport.Host, bus *node.EventBus, saveDir string, lookup ServiceLookup, serviceName string) *Receiver {
	r := &Receiver{
		host:        host,
		eventBus:    bus,
		saveDir:     saveDir,
		lookup:      lookup,
		serviceName: serviceName,
		transfers:   make(map[string]*Transfer),
		pending:     make(map[string]*FileOffer),
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

// resolveService returns the registered services.Service this receiver
// dispatches under, or an error if the registry has no entry under
// serviceName.
func (r *Receiver) resolveService() (*services.Service, error) {
	if r.lookup == nil {
		return nil, fmt.Errorf("filetransfer: no service registry configured")
	}
	svc, ok := r.lookup.Get(r.serviceName)
	if !ok || svc == nil {
		return nil, fmt.Errorf("filetransfer: service %q not registered", r.serviceName)
	}
	return svc, nil
}

// OnFileOffer satisfies services.FileHandler. The libp2p stream handler is
// the primary inbound entry point in v0 and carries the wire-level transfer
// state (chunks, Merkle proof, ACK loop); this method exists so the
// filetransfer.Receiver is a valid registry handler and to allow non-stream
// callers (e.g. tests, future external dispatchers) to push a synthetic
// offer into the same event-bus pipeline. The offer is parked in the
// pending map under svc.FileOffer.ID so AcceptFile/RejectFile can resolve
// it later; with no live libp2p stream there is no chunked transfer that
// follows, but the EventFileOffer payload is the same shape consumers
// expect.
func (r *Receiver) OnFileOffer(_ context.Context, fromAddr string, svcOffer services.FileOffer) error {
	if _, err := r.resolveService(); err != nil {
		return err
	}
	decisionCh := make(chan FileDecision, 1)
	fo := &FileOffer{
		TransferID: svcOffer.ID,
		Filename:   svcOffer.Filename,
		Size:       uint64(svcOffer.Size),
		FromAddr:   fromAddr,
		Decision:   decisionCh,
	}

	r.mu.Lock()
	r.pending[svcOffer.ID] = fo
	r.mu.Unlock()

	r.eventBus.Publish(node.Event{
		Type:    node.EventFileOffer,
		Payload: fo,
	})
	return nil
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

	svc, err := r.resolveService()
	if err != nil {
		return
	}
	if _, ok := svc.AsFile(); !ok {
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
		env := protoc.WrapMessage(pb.MessageType_FILE_REJECT, payload, svc.Identity)
		stream.SetWriteDeadline(time.Now().Add(ChunkTimeout))
		protoc.WriteMsg(stream, env)
		return
	}

	// Send accept
	accept := &pb.FileAccept{TransferId: offer.TransferId}
	payload, _ := proto.Marshal(accept)
	acceptEnv := protoc.WrapMessage(pb.MessageType_FILE_ACCEPT, payload, svc.Identity)
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

	err = r.receiveChunks(stream, xfer, saveDir, svc.Identity)
	if err != nil {
		xfer.mu.Lock()
		xfer.failed = true
		xfer.mu.Unlock()
	}
}

func (r *Receiver) receiveChunks(stream network.Stream, xfer *Transfer, saveDir string, kp *identity.Keypair) error {
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
			ackEnv := protoc.WrapMessage(pb.MessageType_FILE_CHUNK_ACK, ackPayload, kp)
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
