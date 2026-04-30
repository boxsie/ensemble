package filetransfer

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

const DefaultChunkSize = 256 * 1024 // 256 KB

// Chunk represents a single chunk of a file.
type Chunk struct {
	Index uint32
	Data  []byte
	Hash  []byte // SHA-256
}

// Chunker splits a file into fixed-size chunks with SHA-256 hashes.
type Chunker struct {
	path      string
	fileSize  int64
	chunkSize int
	total     uint32
}

// NewChunker creates a chunker for the given file path.
func NewChunker(path string) (*Chunker, error) {
	return NewChunkerWithSize(path, DefaultChunkSize)
}

// NewChunkerWithSize creates a chunker with a custom chunk size.
func NewChunkerWithSize(path string, chunkSize int) (*Chunker, error) {
	if chunkSize <= 0 {
		return nil, fmt.Errorf("chunk size must be positive")
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("file is empty")
	}

	total := uint32((info.Size() + int64(chunkSize) - 1) / int64(chunkSize))

	return &Chunker{
		path:      path,
		fileSize:  info.Size(),
		chunkSize: chunkSize,
		total:     total,
	}, nil
}

// ReadChunk reads the chunk at the given index.
func (c *Chunker) ReadChunk(index uint32) (*Chunk, error) {
	if index >= c.total {
		return nil, fmt.Errorf("chunk index %d out of range [0, %d)", index, c.total)
	}

	f, err := os.Open(c.path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	offset := int64(index) * int64(c.chunkSize)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to chunk %d: %w", index, err)
	}

	buf := make([]byte, c.chunkSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("read chunk %d: %w", index, err)
	}
	buf = buf[:n]

	hash := sha256.Sum256(buf)

	return &Chunk{
		Index: index,
		Data:  buf,
		Hash:  hash[:],
	}, nil
}

// TotalChunks returns the number of chunks in the file.
func (c *Chunker) TotalChunks() uint32 {
	return c.total
}

// FileSize returns the size of the file in bytes.
func (c *Chunker) FileSize() int64 {
	return c.fileSize
}

// ChunkSize returns the chunk size in bytes.
func (c *Chunker) ChunkSize() int {
	return c.chunkSize
}

// Path returns the file path.
func (c *Chunker) Path() string {
	return c.path
}

// AllChunkHashes reads all chunks and returns their SHA-256 hashes.
func (c *Chunker) AllChunkHashes() ([][]byte, error) {
	hashes := make([][]byte, c.total)
	for i := uint32(0); i < c.total; i++ {
		chunk, err := c.ReadChunk(i)
		if err != nil {
			return nil, fmt.Errorf("reading chunk %d: %w", i, err)
		}
		hashes[i] = chunk.Hash
	}
	return hashes, nil
}
