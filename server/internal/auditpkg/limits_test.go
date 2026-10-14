package auditpkg

import (
	"strconv"
	"strings"
	"testing"
)

// TestCanonicalizeRejectsExcessiveObjectKeys checks that MaxObjectKeys counts
// key-value pairs, not each key and each value as separate elements, which
// would halve the effective limit
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

// TestCanonicalizeAcceptsObjectAtExactlyMaxObjectKeys checks that an object with
// MaxObjectKeys pairs, which is 2*MaxObjectKeys JSON tokens, is accepted. A
// per-token count would reject it at MaxObjectKeys/2 pairs.
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

// TestCanonicalizeRejectsUnsafeInteger checks the safe integer range
// [-(2^53-1), 2^53-1]
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

// TestCanonicalizeDoesNotRangeCheckFloats checks that the safe-integer rule
// applies only to integer literals, not to literals with a fraction or exponent
func TestCanonicalizeDoesNotRangeCheckFloats(t *testing.T) {
	if _, err := Canonicalize([]byte(`{"n":1.5}`)); err != nil {
		t.Fatalf("expected float literal to be accepted: %v", err)
	}
}
