package auditpkg

import (
	"strconv"
	"strings"
	"testing"
)

// TestCanonicalizeRejectsExcessiveObjectKeys distinguishes MaxObjectKeys from
// MaxArrayLength: they are separate bounds, and conflating them would
// effectively double the object limit by counting each key AND its value as a
// separate "element".
func TestCanonicalizeRejectsExcessiveObjectKeys(t *testing.T) {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < MaxObjectKeys+10; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k` + strconv.Itoa(i) + `":1`)
	}
	b.WriteByte('}')
	if _, err := Canonicalize([]byte(b.String())); err == nil {
		t.Fatal("expected error for object exceeding MaxObjectKeys")
	}
}

// TestCanonicalizeAcceptsObjectAtHalfNaiveTokenCount proves the fix: an
// object with MaxObjectKeys key-value pairs has 2*MaxObjectKeys raw JSON
// tokens (key + value each), and must still be accepted — a naive
// per-token count would have rejected it at MaxObjectKeys/2 pairs.
func TestCanonicalizeAcceptsObjectAtExactlyMaxObjectKeys(t *testing.T) {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < MaxObjectKeys; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k` + strconv.Itoa(i) + `":1`)
	}
	b.WriteByte('}')
	if _, err := Canonicalize([]byte(b.String())); err != nil {
		t.Fatalf("expected object with exactly MaxObjectKeys pairs to be accepted, got: %v", err)
	}
}

func TestCanonicalizeRejectsExcessiveArrayLength(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < MaxArrayLength+10; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("1")
	}
	b.WriteByte(']')
	if _, err := Canonicalize([]byte(b.String())); err == nil {
		t.Fatal("expected error for array exceeding MaxArrayLength")
	}
}

func TestCanonicalizeRejectsOversizedString(t *testing.T) {
	oversized := `{"a":"` + strings.Repeat("x", MaxStringLength+1) + `"}`
	if _, err := Canonicalize([]byte(oversized)); err == nil {
		t.Fatal("expected error for string exceeding MaxStringLength")
	}
}

func TestCanonicalizeAcceptsStringAtMaxLength(t *testing.T) {
	ok := `{"a":"` + strings.Repeat("x", MaxStringLength) + `"}`
	if _, err := Canonicalize([]byte(ok)); err != nil {
		t.Fatalf("expected string at exactly MaxStringLength to be accepted: %v", err)
	}
}

// TestCanonicalizeRejectsUnsafeInteger enforces the cross-language safe
// integer range [-(2^53-1), 2^53-1].
func TestCanonicalizeRejectsUnsafeInteger(t *testing.T) {
	tooBig := `{"n":9007199254740992}` // 2^53
	if _, err := Canonicalize([]byte(tooBig)); err == nil {
		t.Fatal("expected error for integer exceeding the safe range")
	}
	tooSmall := `{"n":-9007199254740992}`
	if _, err := Canonicalize([]byte(tooSmall)); err == nil {
		t.Fatal("expected error for integer below the negative safe range")
	}
}

func TestCanonicalizeAcceptsIntegerAtSafeBoundary(t *testing.T) {
	atBoundary := `{"n":9007199254740991}` // 2^53 - 1
	if _, err := Canonicalize([]byte(atBoundary)); err != nil {
		t.Fatalf("expected integer at the safe boundary to be accepted: %v", err)
	}
	negBoundary := `{"n":-9007199254740991}`
	if _, err := Canonicalize([]byte(negBoundary)); err != nil {
		t.Fatalf("expected negative integer at the safe boundary to be accepted: %v", err)
	}
}

// TestCanonicalizeDoesNotRangeCheckFloats: the safe-integer rule only
// governs JSON integers; a float literal (has '.' or exponent) is not
// range-checked by this guard (amount-affecting values must be strings
// regardless, which is enforced elsewhere).
func TestCanonicalizeDoesNotRangeCheckFloats(t *testing.T) {
	if _, err := Canonicalize([]byte(`{"n":1.5}`)); err != nil {
		t.Fatalf("expected float literal to be accepted: %v", err)
	}
}
