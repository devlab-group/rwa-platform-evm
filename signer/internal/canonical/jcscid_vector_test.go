package canonical_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rwa-platform/signer/internal/canonical"
	"github.com/rwa-platform/signer/internal/cid"
)

// jcsCIDVector models shared/vectors/jcs-cid.json, shared with server tests: canonical
// bytes, SHA-256 digest and CIDv1 raw/sha2-256 from one input
type jcsCIDVector struct {
	Input              json.RawMessage `json:"input"`
	CanonicalBytesUTF8 string          `json:"canonicalBytesUtf8"`
	CanonicalBytesHex  string          `json:"canonicalBytesHex"`
	SHA256             string          `json:"sha256"`
	CIDv1              string          `json:"cidv1"`
}

// From shared/vectors/jcs-cid.json raw input, reproduce exact canonical bytes, SHA-256
// digest and CIDv1
func TestJCSCIDVector_SharedVectorParity(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "shared", "vectors", "jcs-cid.json"))
	if err != nil {
		t.Fatalf("resolving shared/vectors/jcs-cid.json path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var v jcsCIDVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	got, err := canonical.Canonicalize(v.Input)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}

	if string(got) != v.CanonicalBytesUTF8 {
		t.Errorf("canonicalBytesUtf8 mismatch:\n got  %q\n want %q", got, v.CanonicalBytesUTF8)
	}
	if gotHex := hex.EncodeToString(got); gotHex != v.CanonicalBytesHex {
		t.Errorf("canonicalBytesHex = %s, want %s", gotHex, v.CanonicalBytesHex)
	}

	cidStr, digest := cid.FromCanonical(got)

	gotSHA256 := "0x" + hex.EncodeToString(digest[:])
	if !strings.EqualFold(gotSHA256, v.SHA256) {
		t.Errorf("sha256 = %s, want %s", gotSHA256, v.SHA256)
	}
	if cidStr != v.CIDv1 {
		t.Errorf("cidv1 = %s, want %s", cidStr, v.CIDv1)
	}
}
