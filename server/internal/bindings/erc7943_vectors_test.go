package bindings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// erc7943Vector mirrors shared/vectors/erc7943-abi.json, the frozen ERC-7943
// ABI surface that the Solidity interface and the web ABI fragments are also
// asserted against.
type erc7943Vector struct {
	InterfaceID string `json:"interfaceId"`
	Functions   map[string]struct {
		Signature string `json:"signature"`
		Selector  string `json:"selector"`
	} `json:"functions"`
	Events map[string]struct {
		Signature  string   `json:"signature"`
		Topic0     string   `json:"topic0"`
		Indexed    []string `json:"indexed"`
		NonIndexed []string `json:"nonIndexed"`
	} `json:"events"`
	Errors map[string]struct {
		Signature string `json:"signature"`
		Selector  string `json:"selector"`
	} `json:"errors"`
}

func loadERC7943Vector(t *testing.T) erc7943Vector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "vectors", "erc7943-abi.json"))
	if err != nil {
		t.Fatalf("read shared vector: %v", err)
	}
	var v erc7943Vector
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse shared vector: %v", err)
	}
	return v
}

// TestERC7943VectorsSelfConsistent recomputes every pinned selector and topic0
// from its canonical signature, and rebuilds the ERC-165 interface id from the
// six function selectors, so a hand-edited hex value in the vector cannot pass.
func TestERC7943VectorsSelfConsistent(t *testing.T) {
	v := loadERC7943Vector(t)

	var interfaceID [4]byte
	for name, fn := range v.Functions {
		want := "0x" + common.Bytes2Hex(crypto.Keccak256([]byte(fn.Signature))[:4])
		if fn.Selector != want {
			t.Errorf("%s: pinned selector %s, signature hashes to %s", name, fn.Selector, want)
		}
		for i, b := range common.FromHex(fn.Selector) {
			interfaceID[i] ^= b
		}
	}
	if got := "0x" + common.Bytes2Hex(interfaceID[:]); got != v.InterfaceID {
		t.Errorf("interface id: pinned %s, selectors xor to %s", v.InterfaceID, got)
	}

	for name, ev := range v.Events {
		want := crypto.Keccak256Hash([]byte(ev.Signature))
		if common.HexToHash(ev.Topic0) != want {
			t.Errorf("%s: pinned topic0 %s, signature hashes to %s", name, ev.Topic0, want.Hex())
		}
	}
	for name, e := range v.Errors {
		want := "0x" + common.Bytes2Hex(crypto.Keccak256([]byte(e.Signature))[:4])
		if e.Selector != want {
			t.Errorf("%s: pinned selector %s, signature hashes to %s", name, e.Selector, want)
		}
	}
}

// TestERC7943VectorsMatchTokenBindings holds the hand-written RWAToken event
// ABI to the same frozen vector: topic0, and which argument is indexed.
func TestERC7943VectorsMatchTokenBindings(t *testing.T) {
	v := loadERC7943Vector(t)
	token := NewRWAToken()

	if len(v.Events) != len(token.ABI.Events) {
		t.Fatalf("vector pins %d events, bindings declare %d", len(v.Events), len(token.ABI.Events))
	}
	for name, want := range v.Events {
		got, ok := token.ABI.Events[name]
		if !ok {
			t.Errorf("bindings are missing event %s", name)
			continue
		}
		if got.ID != common.HexToHash(want.Topic0) {
			t.Errorf("%s: topic0 %s, want %s", name, got.ID.Hex(), want.Topic0)
		}
		if got.Sig != want.Signature {
			t.Errorf("%s: signature %q, want %q", name, got.Sig, want.Signature)
		}
		var indexed, nonIndexed []string
		for _, in := range got.Inputs {
			if in.Indexed {
				indexed = append(indexed, in.Name)
			} else {
				nonIndexed = append(nonIndexed, in.Name)
			}
		}
		assertNames(t, name+" indexed", indexed, want.Indexed)
		assertNames(t, name+" non-indexed", nonIndexed, want.NonIndexed)
	}
}

func assertNames(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %v, want %v", what, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: %v, want %v", what, got, want)
			return
		}
	}
}
