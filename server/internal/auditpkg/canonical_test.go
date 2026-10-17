package auditpkg

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	gocid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

func TestCanonicalizeSortsKeysAndStripsWhitespace(t *testing.T) {
	in := []byte(`{ "b": 2, "a": 1, "c": { "y": true, "x": false } }`)
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("Canonicalize error: %v", err)
	}
	want := `{"a":1,"b":2,"c":{"x":false,"y":true}}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestCanonicalizeUnicodeKeyOrdering covers U+00E9, U+20AC and U+FB2A. Each occupies
// one UTF-16 unit, so code point and RFC 8785 code-unit ordering agree. The jcs library
// tests surrogate pairs.
func TestCanonicalizeUnicodeKeyOrdering(t *testing.T) {
	in := []byte(`{"` + "€" + `":"euro","` + "é" + `":"eacute","` + "שׁ" + `":"shin"}`)
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("Canonicalize error: %v", err)
	}
	want := `{"` + "é" + `":"eacute","` + "€" + `":"euro","` + "שׁ" + `":"shin"}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestCanonicalizeRejectsDuplicateKeys(t *testing.T) {
	in := []byte(`{"a":1,"a":2}`)
	if _, err := Canonicalize(in); err == nil {
		t.Fatal("expected error for duplicate key")
	}
}

func TestCanonicalizeRejectsInvalidUTF8(t *testing.T) {
	in := []byte{'{', '"', 'a', '"', ':', '"', 0xff, 0xfe, '"', '}'}
	if _, err := Canonicalize(in); err == nil {
		t.Fatal("expected error for invalid UTF-8")
	}
}

func TestCanonicalizeRejectsExcessiveDepth(t *testing.T) {
	// Build a deeply nested array beyond MaxObjectDepth
	var b strings.Builder
	depth := MaxObjectDepth + 10
	for i := 0; i < depth; i++ {
		b.WriteByte('[')
	}
	for i := 0; i < depth; i++ {
		b.WriteByte(']')
	}
	if _, err := Canonicalize([]byte(b.String())); err == nil {
		t.Fatal("expected error for excessive nesting depth")
	}
}

func TestCanonicalizeRejectsEmpty(t *testing.T) {
	if _, err := Canonicalize(nil); err == nil {
		t.Fatal("expected error for empty document")
	}
}

// Check the spec's SHA-256 digest against the standard library, independently of Digest
func TestDigestIsSHA256OfTheCanonicalBytes(t *testing.T) {
	canon, err := Canonicalize([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"a":1,"b":2}` {
		t.Fatalf("canonical bytes = %s", canon)
	}
	want := sha256.Sum256([]byte(`{"a":1,"b":2}`))
	if got := Digest(canon); got != want {
		t.Errorf("Digest = %s, want %s", hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}
}

// The CID is a CIDv1 with the raw codec and a sha2-256 multihash, derived here with
// go-cid and go-multihash; "bafkrei" is the base32 prefix of that combination
func TestCIDv1RawIsRawCodecSha256(t *testing.T) {
	canon, err := Canonicalize([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	mh, err := multihash.Sum(canon, multihash.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	want := gocid.NewCidV1(gocid.Raw, mh).String()
	got, err := CIDv1Raw(canon)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("CIDv1Raw = %s, want %s", got, want)
	}
	if !strings.HasPrefix(got, "bafkrei") {
		t.Errorf("CID %s lacks the base32 raw/sha2-256 prefix bafkrei", got)
	}
}

func TestCanonicalizeAndDigestConsistentWithSteps(t *testing.T) {
	raw := []byte(`{"z":1,"a":"hello"}`)
	canon, digest, cidStr, err := CanonicalizeAndDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantCanon, err := Canonicalize(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != string(wantCanon) {
		t.Errorf("canonical mismatch")
	}
	if digest != Digest(wantCanon) {
		t.Errorf("digest mismatch")
	}
	wantCID, err := CIDv1Raw(wantCanon)
	if err != nil {
		t.Fatal(err)
	}
	if cidStr != wantCID {
		t.Errorf("cid mismatch")
	}
}
