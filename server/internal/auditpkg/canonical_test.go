package auditpkg

import (
	"encoding/hex"
	"strings"
	"testing"
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

// TestCanonicalizeUnicodeKeyOrdering checks BMP-codepoint key sorting
// (U+00E9 'e-acute' < U+20AC euro sign < U+FB2A hebrew shin, all single
// UTF-16 code units so codepoint order and UTF-16-code-unit order agree —
// this avoids hand-coding the RFC 8785 surrogate-pair edge case, which the
// library's own upstream test suite already covers).
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
	// Build a deeply nested array beyond MaxObjectDepth.
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

func TestDigestIsDeterministic(t *testing.T) {
	canon, err := Canonicalize([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	d1 := Digest(canon)
	d2 := Digest(canon)
	if d1 != d2 {
		t.Fatal("digest not deterministic")
	}
	if got := hex.EncodeToString(d1[:]); len(got) != 64 {
		t.Errorf("digest hex length = %d, want 64", len(got))
	}
}

func TestCIDv1RawIsStableAndCodecRaw(t *testing.T) {
	canon, err := Canonicalize([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	c1, err := CIDv1Raw(canon)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := CIDv1Raw(canon)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Errorf("CID not stable: %s != %s", c1, c2)
	}
	if !strings.HasPrefix(c1, "b") {
		// base32 (lower) CIDv1 strings conventionally start with 'b'.
		t.Errorf("CID %s does not look like a base32 CIDv1", c1)
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
