package filetransfer

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func createTestFile(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile.bin")
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChunkerBasic(t *testing.T) {
	// 1 MB file with 256 KB chunks → 4 chunks
	path := createTestFile(t, 1024*1024)
	c, err := NewChunker(path)
	if err != nil {
		t.Fatal(err)
	}

	if c.TotalChunks() != 4 {
		t.Fatalf("expected 4 chunks, got %d", c.TotalChunks())
	}
	if c.FileSize() != 1024*1024 {
		t.Fatalf("expected file size 1048576, got %d", c.FileSize())
	}
	if c.ChunkSize() != DefaultChunkSize {
		t.Fatalf("expected chunk size %d, got %d", DefaultChunkSize, c.ChunkSize())
	}
}

func TestChunkerPartialLastChunk(t *testing.T) {
	// 300 KB file with 256 KB chunks → 2 chunks, second is 44 KB
	size := 300 * 1024
	path := createTestFile(t, size)
	c, err := NewChunker(path)
	if err != nil {
		t.Fatal(err)
	}

	if c.TotalChunks() != 2 {
		t.Fatalf("expected 2 chunks, got %d", c.TotalChunks())
	}

	chunk0, err := c.ReadChunk(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk0.Data) != DefaultChunkSize {
		t.Fatalf("chunk 0: expected %d bytes, got %d", DefaultChunkSize, len(chunk0.Data))
	}

	chunk1, err := c.ReadChunk(1)
	if err != nil {
		t.Fatal(err)
	}
	expected := size - DefaultChunkSize
	if len(chunk1.Data) != expected {
		t.Fatalf("chunk 1: expected %d bytes, got %d", expected, len(chunk1.Data))
	}
}

func TestChunkerHashDeterministic(t *testing.T) {
	path := createTestFile(t, 512*1024)
	c, err := NewChunker(path)
	if err != nil {
		t.Fatal(err)
	}

	chunk1, _ := c.ReadChunk(0)
	chunk2, _ := c.ReadChunk(0)

	if string(chunk1.Hash) != string(chunk2.Hash) {
		t.Fatal("same chunk should produce same hash")
	}
}

func TestChunkerHashCorrect(t *testing.T) {
	path := createTestFile(t, 100)
	c, err := NewChunkerWithSize(path, 100)
	if err != nil {
		t.Fatal(err)
	}

	chunk, _ := c.ReadChunk(0)

	// Verify hash manually
	data, _ := os.ReadFile(path)
	expected := sha256.Sum256(data)
	if string(chunk.Hash) != string(expected[:]) {
		t.Fatal("chunk hash doesn't match manual SHA-256")
	}
}

func TestChunkerOutOfRange(t *testing.T) {
	path := createTestFile(t, 100)
	c, err := NewChunkerWithSize(path, 100)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.ReadChunk(1); err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestChunkerEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.bin")
	os.WriteFile(path, []byte{}, 0644)

	_, err := NewChunker(path)
	if err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestChunkerDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := NewChunker(dir)
	if err == nil {
		t.Fatal("expected error for directory")
	}
}

func TestChunkerMissingFile(t *testing.T) {
	_, err := NewChunker("/nonexistent/file.bin")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestChunkerInvalidChunkSize(t *testing.T) {
	path := createTestFile(t, 100)
	_, err := NewChunkerWithSize(path, 0)
	if err == nil {
		t.Fatal("expected error for zero chunk size")
	}
	_, err = NewChunkerWithSize(path, -1)
	if err == nil {
		t.Fatal("expected error for negative chunk size")
	}
}

func TestChunkerAllChunkHashes(t *testing.T) {
	path := createTestFile(t, 512*1024) // 2 chunks
	c, err := NewChunker(path)
	if err != nil {
		t.Fatal(err)
	}

	hashes, err := c.AllChunkHashes()
	if err != nil {
		t.Fatal(err)
	}

	if len(hashes) != 2 {
		t.Fatalf("expected 2 hashes, got %d", len(hashes))
	}

	// Verify each hash matches individual ReadChunk
	for i := uint32(0); i < 2; i++ {
		chunk, _ := c.ReadChunk(i)
		if string(hashes[i]) != string(chunk.Hash) {
			t.Fatalf("hash mismatch at index %d", i)
		}
	}
}

func TestChunkerExactMultiple(t *testing.T) {
	// File size exactly divisible by chunk size
	path := createTestFile(t, DefaultChunkSize*3)
	c, err := NewChunker(path)
	if err != nil {
		t.Fatal(err)
	}

	if c.TotalChunks() != 3 {
		t.Fatalf("expected 3 chunks, got %d", c.TotalChunks())
	}

	// All chunks should be full size
	for i := uint32(0); i < 3; i++ {
		chunk, err := c.ReadChunk(i)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk.Data) != DefaultChunkSize {
			t.Fatalf("chunk %d: expected %d bytes, got %d", i, DefaultChunkSize, len(chunk.Data))
		}
	}
}

func TestChunkerSmallChunkSize(t *testing.T) {
	// Small chunk size for testing: 10 bytes, 25 byte file → 3 chunks
	path := createTestFile(t, 25)
	c, err := NewChunkerWithSize(path, 10)
	if err != nil {
		t.Fatal(err)
	}

	if c.TotalChunks() != 3 {
		t.Fatalf("expected 3 chunks, got %d", c.TotalChunks())
	}

	chunk2, _ := c.ReadChunk(2)
	if len(chunk2.Data) != 5 {
		t.Fatalf("last chunk: expected 5 bytes, got %d", len(chunk2.Data))
	}
}
