package filetransfer

import (
	"crypto/sha256"
	"testing"
)

func makeHashes(n int) [][]byte {
	hashes := make([][]byte, n)
	for i := range hashes {
		h := sha256.Sum256([]byte{byte(i)})
		hashes[i] = h[:]
	}
	return hashes
}

func TestMerkleRootDeterministic(t *testing.T) {
	hashes := makeHashes(4)
	tree1, _ := BuildMerkleTree(hashes)
	tree2, _ := BuildMerkleTree(hashes)

	if string(tree1.Root()) != string(tree2.Root()) {
		t.Fatal("same input should produce same root")
	}
}

func TestMerkleRootChangesWithData(t *testing.T) {
	tree1, _ := BuildMerkleTree(makeHashes(4))
	tree2, _ := BuildMerkleTree(makeHashes(5))

	if string(tree1.Root()) == string(tree2.Root()) {
		t.Fatal("different inputs should produce different roots")
	}
}

func TestMerkleSingleLeaf(t *testing.T) {
	hashes := makeHashes(1)
	tree, err := BuildMerkleTree(hashes)
	if err != nil {
		t.Fatal(err)
	}

	// Single leaf: root = H(leaf || leaf) since padded to size 1 → duplicated
	expected := hashPair(hashes[0], hashes[0])
	if string(tree.Root()) != string(expected) {
		t.Fatal("single leaf root mismatch")
	}

	proof, err := tree.Proof(0)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyProof(hashes[0], 0, proof, tree.Root()) {
		t.Fatal("proof failed for single leaf")
	}
}

func TestMerkleProofAllLeaves(t *testing.T) {
	for _, n := range []int{2, 3, 4, 5, 7, 8, 16} {
		hashes := makeHashes(n)
		tree, err := BuildMerkleTree(hashes)
		if err != nil {
			t.Fatalf("n=%d: build failed: %v", n, err)
		}

		if tree.LeafCount() != n {
			t.Fatalf("n=%d: expected %d leaves, got %d", n, n, tree.LeafCount())
		}

		for i := 0; i < n; i++ {
			proof, err := tree.Proof(i)
			if err != nil {
				t.Fatalf("n=%d, i=%d: proof generation failed: %v", n, i, err)
			}
			if !VerifyProof(hashes[i], i, proof, tree.Root()) {
				t.Fatalf("n=%d, i=%d: valid proof failed verification", n, i)
			}
		}
	}
}

func TestMerkleTamperedChunkFails(t *testing.T) {
	hashes := makeHashes(4)
	tree, _ := BuildMerkleTree(hashes)

	proof, _ := tree.Proof(0)

	// Tamper with the chunk hash
	tampered := sha256.Sum256([]byte("tampered"))
	if VerifyProof(tampered[:], 0, proof, tree.Root()) {
		t.Fatal("tampered chunk hash should fail verification")
	}
}

func TestMerkleTamperedProofFails(t *testing.T) {
	hashes := makeHashes(4)
	tree, _ := BuildMerkleTree(hashes)

	proof, _ := tree.Proof(0)

	// Tamper with a proof sibling
	tampered := make([][]byte, len(proof))
	copy(tampered, proof)
	bad := sha256.Sum256([]byte("bad"))
	tampered[0] = bad[:]

	if VerifyProof(hashes[0], 0, tampered, tree.Root()) {
		t.Fatal("tampered proof should fail verification")
	}
}

func TestMerkleWrongIndexFails(t *testing.T) {
	hashes := makeHashes(4)
	tree, _ := BuildMerkleTree(hashes)

	// Get proof for index 0 but verify as index 1
	proof, _ := tree.Proof(0)
	if VerifyProof(hashes[0], 1, proof, tree.Root()) {
		t.Fatal("wrong index should fail verification")
	}
}

func TestMerkleProofOutOfRange(t *testing.T) {
	hashes := makeHashes(4)
	tree, _ := BuildMerkleTree(hashes)

	if _, err := tree.Proof(-1); err == nil {
		t.Fatal("expected error for negative index")
	}
	if _, err := tree.Proof(4); err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestMerkleEmptyInput(t *testing.T) {
	_, err := BuildMerkleTree(nil)
	if err == nil {
		t.Fatal("expected error for nil input")
	}
	_, err = BuildMerkleTree([][]byte{})
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestMerkleVerifyEmptyInputs(t *testing.T) {
	if VerifyProof(nil, 0, nil, []byte("root")) {
		t.Fatal("nil chunk hash should fail")
	}
	if VerifyProof([]byte("hash"), 0, nil, nil) {
		t.Fatal("nil root should fail")
	}
}

func TestMerkleOddLeafDuplication(t *testing.T) {
	// 3 leaves: padded to 4 by duplicating last
	hashes := makeHashes(3)
	tree, _ := BuildMerkleTree(hashes)

	// Manually compute expected root:
	// Level 0 (leaves): h0, h1, h2, h2 (duplicated)
	// Level 1: H(h0||h1), H(h2||h2)
	// Level 2 (root): H(H(h0||h1) || H(h2||h2))
	left := hashPair(hashes[0], hashes[1])
	right := hashPair(hashes[2], hashes[2])
	expectedRoot := hashPair(left, right)

	if string(tree.Root()) != string(expectedRoot) {
		t.Fatal("odd leaf duplication root mismatch")
	}
}

func TestMerkleIntegrationWithChunker(t *testing.T) {
	// Create a file, chunk it, build Merkle tree, verify all proofs
	path := createTestFile(t, 700*1024) // ~3 chunks at 256KB
	c, err := NewChunker(path)
	if err != nil {
		t.Fatal(err)
	}

	hashes, err := c.AllChunkHashes()
	if err != nil {
		t.Fatal(err)
	}

	tree, err := BuildMerkleTree(hashes)
	if err != nil {
		t.Fatal(err)
	}

	if tree.LeafCount() != int(c.TotalChunks()) {
		t.Fatalf("leaf count %d != chunk count %d", tree.LeafCount(), c.TotalChunks())
	}

	for i := 0; i < tree.LeafCount(); i++ {
		proof, err := tree.Proof(i)
		if err != nil {
			t.Fatalf("proof for chunk %d: %v", i, err)
		}
		if !VerifyProof(hashes[i], i, proof, tree.Root()) {
			t.Fatalf("chunk %d proof failed", i)
		}
	}
}
