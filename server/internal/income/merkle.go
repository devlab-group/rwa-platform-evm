package income

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Leaf is one (account, amount) pair of an income Merkle tree
type Leaf struct {
	Account common.Address
	Amount  *big.Int
}

// LeafHash is OpenZeppelin StandardMerkleTree's leaf for ["address","uint256"]:
// keccak256(keccak256(abi.encode(account, amount)))
func LeafHash(account common.Address, amount *big.Int) [32]byte {
	inner := crypto.Keccak256(common.LeftPadBytes(account.Bytes(), 32), common.LeftPadBytes(amount.Bytes(), 32))
	return crypto.Keccak256Hash(inner)
}

// Tree reproduces OpenZeppelin StandardMerkleTree.of(values, ["address","uint256"]),
// the tree IncomeDistributor.claim verifies against. Leaves are sorted by hash and
// stored from the end of a (2n-1)-element array, so a node's children sit at 2i+1 and
// 2i+2 and parents hash their children in sorted order.
type Tree struct {
	nodes [][32]byte
	// byHash maps a leaf hash to its index in nodes
	byHash map[[32]byte]int
}

// BuildTree builds the tree for leaves, which must be non-empty with non-negative
// amounts below 2^256 and distinct leaf hashes (distinct accounts guarantee that)
func BuildTree(leaves []Leaf) (*Tree, error) {
	if len(leaves) == 0 {
		return nil, errors.New("income: a Merkle tree needs at least one leaf")
	}
	hashes := make([][32]byte, len(leaves))
	for i, l := range leaves {
		if l.Amount == nil || l.Amount.Sign() < 0 || l.Amount.BitLen() > 256 {
			return nil, fmt.Errorf("income: leaf %d has an amount outside uint256", i)
		}
		hashes[i] = LeafHash(l.Account, l.Amount)
	}
	sort.Slice(hashes, func(i, j int) bool { return bytes.Compare(hashes[i][:], hashes[j][:]) < 0 })
	for i := 1; i < len(hashes); i++ {
		if hashes[i] == hashes[i-1] {
			return nil, errors.New("income: duplicate Merkle leaf")
		}
	}

	n := len(hashes)
	nodes := make([][32]byte, 2*n-1)
	byHash := make(map[[32]byte]int, n)
	for i, h := range hashes {
		nodes[len(nodes)-1-i] = h
		byHash[h] = len(nodes) - 1 - i
	}
	for i := len(nodes) - 1 - n; i >= 0; i-- {
		nodes[i] = hashPair(nodes[2*i+1], nodes[2*i+2])
	}
	return &Tree{nodes: nodes, byHash: byHash}, nil
}

// hashPair hashes two nodes in ascending byte order
func hashPair(a, b [32]byte) [32]byte {
	if bytes.Compare(a[:], b[:]) > 0 {
		a, b = b, a
	}
	return crypto.Keccak256Hash(a[:], b[:])
}

// Root returns the Merkle root
func (t *Tree) Root() [32]byte { return t.nodes[0] }

// Proof returns the sibling path for the leaf (account, amount), or an error when the
// tree holds no such leaf
func (t *Tree) Proof(account common.Address, amount *big.Int) ([][32]byte, error) {
	idx, ok := t.byHash[LeafHash(account, amount)]
	if !ok {
		return nil, errors.New("income: leaf is not in the tree")
	}
	proof := [][32]byte{}
	for idx > 0 {
		sibling := idx + 1
		if idx%2 == 0 {
			sibling = idx - 1
		}
		proof = append(proof, t.nodes[sibling])
		idx = (idx - 1) / 2
	}
	return proof, nil
}

// VerifyProof folds proof over leaf the way MerkleProof.verify does and compares with root
func VerifyProof(root [32]byte, leaf [32]byte, proof [][32]byte) bool {
	h := leaf
	for _, p := range proof {
		h = hashPair(h, p)
	}
	return h == root
}
