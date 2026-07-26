package auditpkg

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestJCSCIDGoldenVector cross-checks Canonicalize/Digest/CIDv1Raw against
// the FROZEN shared/vectors/jcs-cid.json, the lead's cross-language proof
// that this package's RFC 8785 canonicalization and CID convention agree
// with the signer/TS client for a document exercising key sorting, nested
// objects/arrays, and array-order preservation.
func TestJCSCIDGoldenVector(t *testing.T) {
	p := filepath.Join("..", "..", "..", "shared", "vectors", "jcs-cid.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("golden vector not found: %v", err)
	}
	var v struct {
		Input              json.RawMessage `json:"input"`
		CanonicalBytesUtf8 string          `json:"canonicalBytesUtf8"`
		CanonicalBytesHex  string          `json:"canonicalBytesHex"`
		SHA256             string          `json:"sha256"`
		CIDv1              string          `json:"cidv1"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal vector: %v", err)
	}

	canonical, digest, cidStr, err := CanonicalizeAndDigest(v.Input)
	if err != nil {
		t.Fatalf("CanonicalizeAndDigest: %v", err)
	}

	if string(canonical) != v.CanonicalBytesUtf8 {
		t.Errorf("canonical bytes mismatch:\n got  %s\n want %s", canonical, v.CanonicalBytesUtf8)
	}
	if hex.EncodeToString(canonical) != v.CanonicalBytesHex {
		t.Errorf("canonical bytes hex mismatch:\n got  %s\n want %s", hex.EncodeToString(canonical), v.CanonicalBytesHex)
	}
	if got := "0x" + hex.EncodeToString(digest[:]); got != v.SHA256 {
		t.Errorf("sha256 mismatch: got %s want %s", got, v.SHA256)
	}
	if cidStr != v.CIDv1 {
		t.Errorf("cidv1 mismatch: got %s want %s", cidStr, v.CIDv1)
	}
}
