# Phase 6: File Transfer — T-38 to T-44

## Goal
Chunked, integrity-verified file transfer between peers.

---

## T-38: File chunker
Split files into fixed-size chunks with hashes.
- Default chunk size: 256KB
- `Chunker.ReadChunk(index) → Chunk { Index, Data, Hash }`
- `Chunker.TotalChunks()`, `Chunker.FileSize()`
- SHA-256 hash per chunk
- **Files**: `internal/filetransfer/chunker.go`, `internal/filetransfer/chunker_test.go`
- **Verify**: Chunk a known file, verify chunk count = ceil(size/256KB). Hash is deterministic.

## T-39: Merkle tree
Build tree from chunk hashes, generate/verify per-chunk proofs.
- `BuildMerkleTree(chunkHashes) → MerkleTree`
- `Root() → []byte`
- `Proof(chunkIndex) → [][]byte` (sibling hashes leaf→root)
- `VerifyProof(chunkHash, chunkIndex, proof, root) → bool`
- Odd leaf count: duplicate last leaf
- **Files**: `internal/filetransfer/merkle.go`, `internal/filetransfer/merkle_test.go`
- **Verify**: Build tree for known data, verify root is deterministic. Generate proof for each chunk, verify each. Tampered chunk fails verification.

## T-40: File transfer sender
Offer files and send chunks with flow control.
- `OfferFile(ctx, peerID, filePath) → Transfer` — chunk, build Merkle tree, send FileOffer
- Wait for FileAccept/FileReject
- Send chunks with sliding window (8 in-flight), wait for ACKs
- Retransmit on failed verification (max 3 retries)
- `CancelTransfer(id)`
- **Files**: `internal/filetransfer/sender.go`, `internal/filetransfer/sender_test.go`
- **Verify**: Send a file, all chunks ACK'd. Cancel mid-transfer. Retransmit corrupted chunk.

## T-41: File transfer receiver
Accept offers, receive chunks, verify, reassemble.
- Register stream handler for `/ensemble/filetransfer/1.0.0`
- Accept/reject offers via callback
- Per-chunk: verify SHA-256 hash, verify Merkle proof against root
- Send ChunkAck (valid=true/false)
- After all chunks: reassemble file, verify SHA-256 of complete file
- **Files**: `internal/filetransfer/receiver.go`, `internal/filetransfer/receiver_test.go`
- **Verify**: Receive a file, SHA-256 matches original. Corrupt chunk triggers retransmit. Full file hash verified.

## T-42: Transfer progress tracking
Real-time speed, ETA, percentage.
- `Progress.Update(chunkBytes)` — rolling average speed
- `Progress.Percent()`, `Progress.String()` → "45% (12.3 MB/s, ~2m left)"
- **Files**: `internal/filetransfer/progress.go`, `internal/filetransfer/progress_test.go`
- **Verify**: Simulate chunk arrivals, verify percentage and speed calculations.

## T-43: Wire file transfer into node + gRPC
Expose file transfer via gRPC API.
- `SendFile` RPC → sender service, return streaming TransferProgress
- `AcceptFile`/`RejectFile` RPCs for inbound offers
- File offers → EventBus → gRPC Subscribe stream
- **Files**: Update `internal/node/node.go`, `internal/api/server.go`
- **Verify**: `grpcurl` calls `SendFile`, receives progress stream. Inbound offers appear on Subscribe.

## T-44: TUI file picker + transfer screens
UI for selecting files and monitoring transfers.
- File picker: browse filesystem, select file to send
- Transfer screen: active transfers with progress bars, speed, ETA
- File offer notification: "Bob is sending photo.jpg (12MB). Accept? [y/n]"
- Cancel transfer with `c`
- **Files**: `internal/ui/screens/filepicker.go`, `internal/ui/screens/transfer.go`
- **Verify**: Pick a file from TUI, see progress bar. Accept incoming file, see it download.
