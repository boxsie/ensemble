package filetransfer

import (
	"crypto/sha256"
	"fmt"
)

// MerkleTree is a binary hash tree built from chunk hashes.
// Stored as a flat array (heap layout): index 1 = root,
// children of node i at 2i and 2i+1, leaves at [leafStart .. 2*leafStart-1].
type MerkleTree struct {
	leaves    [][]byte // original leaf hashes (before padding)
	nodes     [][]byte // heap-indexed: nodes[1] = root
	leafStart int      // index of first leaf in nodes[]
}

// BuildMerkleTree constructs a Merkle tree from chunk hashes.
// Odd leaf counts are handled by duplicating the last leaf to the next power of 2.
func BuildMerkleTree(chunkHashes [][]byte) (*MerkleTree, error) {
	if len(chunkHashes) == 0 {
		return nil, fmt.Errorf("no chunk hashes provided")
	}

	// Pad to next power of 2, minimum 2 (single leaf gets duplicated)
	n := len(chunkHashes)
	size := 2
	for size < n {
		size *= 2
	}

	// Flat heap array: indices [1 .. 2*size-1], index 0 unused.
	nodes := make([][]byte, 2*size)

	// Fill leaf positions [size .. 2*size-1]
	for i := 0; i < n; i++ {
		nodes[size+i] = chunkHashes[i]
	}
	// Pad remaining leaves by duplicating the last hash
	for i := n; i < size; i++ {
		nodes[size+i] = chunkHashes[n-1]
	}

	// Build internal nodes bottom-up
	for i := size - 1; i >= 1; i-- {
		nodes[i] = hashPair(nodes[2*i], nodes[2*i+1])
	}

	leaves := make([][]byte, n)
	copy(leaves, chunkHashes)

	return &MerkleTree{
		leaves:    leaves,
		nodes:     nodes,
		leafStart: size,
	}, nil
}

func hashPair(left, right []byte) []byte {
	h := sha256.New()
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// Root returns the Merkle root hash.
func (t *MerkleTree) Root() []byte {
	return t.nodes[1]
}

// LeafCount returns the number of original leaves (before padding).
func (t *MerkleTree) LeafCount() int {
	return len(t.leaves)
}

// Proof generates a Merkle proof for the chunk at the given index.
// Returns sibling hashes from leaf to root.
func (t *MerkleTree) Proof(chunkIndex int) ([][]byte, error) {
	if chunkIndex < 0 || chunkIndex >= len(t.leaves) {
		return nil, fmt.Errorf("chunk index %d out of range [0, %d)", chunkIndex, len(t.leaves))
	}

	pos := t.leafStart + chunkIndex
	var proof [][]byte
	for pos > 1 {
		sibling := pos ^ 1 // flip lowest bit to get sibling
		proof = append(proof, t.nodes[sibling])
		pos /= 2
	}
	return proof, nil
}

// VerifyProof checks that a chunk hash, combined with the proof siblings,
// produces the expected Merkle root.
func VerifyProof(chunkHash []byte, chunkIndex int, proof [][]byte, root []byte) bool {
	if len(chunkHash) == 0 || len(root) == 0 {
		return false
	}

	// Reconstruct the leaf's position in the flat tree.
	// Tree depth = len(proof), so leafStart = 2^depth.
	leafStart := 1
	for range proof {
		leafStart *= 2
	}
	pos := leafStart + chunkIndex

	current := chunkHash
	for _, sibling := range proof {
		if pos%2 == 0 {
			current = hashPair(current, sibling)
		} else {
			current = hashPair(sibling, current)
		}
		pos /= 2
	}

	return string(current) == string(root)
}
