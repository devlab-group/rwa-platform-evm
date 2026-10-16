package income

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type merkleVectorFile struct {
	Cases []struct {
		Name    string `json:"name"`
		Root    string `json:"root"`
		Entries []struct {
			Account string   `json:"account"`
			Amount  string   `json:"amount"`
			Leaf    string   `json:"leaf"`
			Proof   []string `json:"proof"`
		} `json:"entries"`
	} `json:"cases"`
}

func hex32(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		t.Fatalf("bad bytes32 %q: %v", s, err)
	}
	var out [32]byte
	copy(out[:], b)
	return out
}

// TestIncomeMerkleVectors asserts every root, leaf and proof of shared/vectors/income-merkle.json,
// generated with @openzeppelin/merkle-tree
func TestIncomeMerkleVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "vectors", "income-merkle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vf merkleVectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	if len(vf.Cases) == 0 {
		t.Fatal("vector file has no cases")
	}
	for _, c := range vf.Cases {
		t.Run(c.Name, func(t *testing.T) {
			leaves := make([]Leaf, len(c.Entries))
			for i, e := range c.Entries {
				amount, ok := new(big.Int).SetString(e.Amount, 10)
				if !ok {
					t.Fatalf("bad amount %q", e.Amount)
				}
				leaves[i] = Leaf{Account: common.HexToAddress(e.Account), Amount: amount}
			}
			tree, err := BuildTree(leaves)
			if err != nil {
				t.Fatal(err)
			}
			if tree.Root() != hex32(t, c.Root) {
				t.Errorf("root = 0x%x, want %s", tree.Root(), c.Root)
			}
			for i, e := range c.Entries {
				if got := LeafHash(leaves[i].Account, leaves[i].Amount); got != hex32(t, e.Leaf) {
					t.Errorf("entry %d leaf = 0x%x, want %s", i, got, e.Leaf)
				}
				proof, err := tree.Proof(leaves[i].Account, leaves[i].Amount)
				if err != nil {
					t.Fatalf("entry %d proof: %v", i, err)
				}
				if len(proof) != len(e.Proof) {
					t.Fatalf("entry %d proof length = %d, want %d", i, len(proof), len(e.Proof))
				}
				for j, p := range e.Proof {
					if proof[j] != hex32(t, p) {
						t.Errorf("entry %d proof[%d] = 0x%x, want %s", i, j, proof[j], p)
					}
				}
				if !VerifyProof(tree.Root(), hex32(t, e.Leaf), proof) {
					t.Errorf("entry %d proof does not verify", i)
				}
			}
		})
	}
}

func TestMerkleTreeRejectsBadInput(t *testing.T) {
	if _, err := BuildTree(nil); err == nil {
		t.Error("empty leaf set accepted")
	}
	a := common.HexToAddress("0x01")
	if _, err := BuildTree([]Leaf{{a, big.NewInt(1)}, {a, big.NewInt(1)}}); err == nil {
		t.Error("duplicate leaf accepted")
	}
	if _, err := BuildTree([]Leaf{{a, big.NewInt(-1)}}); err == nil {
		t.Error("negative amount accepted")
	}
	tree, err := BuildTree([]Leaf{{a, big.NewInt(5)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Proof(a, big.NewInt(6)); err == nil {
		t.Error("proof for a leaf that is not in the tree")
	}
	if _, err := tree.Proof(common.HexToAddress("0x02"), big.NewInt(5)); err == nil {
		t.Error("proof for another account's leaf")
	}
}

// Every leaf of trees of all small sizes must verify, covering the odd and even shapes
// the (2n-1)-array layout produces
func TestMerkleProofsVerifyForAllSizes(t *testing.T) {
	for n := 1; n <= 17; n++ {
		leaves := make([]Leaf, n)
		for i := range leaves {
			leaves[i] = Leaf{Account: common.BigToAddress(big.NewInt(int64(i + 1))), Amount: big.NewInt(int64(1000 + i))}
		}
		tree, err := BuildTree(leaves)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range leaves {
			proof, err := tree.Proof(l.Account, l.Amount)
			if err != nil {
				t.Fatalf("n=%d: %v", n, err)
			}
			if !VerifyProof(tree.Root(), LeafHash(l.Account, l.Amount), proof) {
				t.Fatalf("n=%d: proof for %s does not verify", n, l.Account)
			}
		}
	}
}
