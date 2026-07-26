package canonical

import "testing"

func TestCanonicalize_KeyOrdering(t *testing.T) {
	in := []byte(`{ "b": 1, "a": 2, "c": { "z": 1, "y": 2 } }`)
	want := `{"a":2,"b":1,"c":{"y":2,"z":1}}`
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalize_WhitespaceInsignificant(t *testing.T) {
	a, err := Canonicalize([]byte(`{"a":1,"b":[1,2,3]}`))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	b, err := Canonicalize([]byte("  {\n \"b\" : [1,\t2, 3]  ,\n\"a\":1\n}\n"))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("expected identical canonical bytes, got %q vs %q", a, b)
	}
}

func TestCanonicalize_DuplicateKeyRejected(t *testing.T) {
	_, err := Canonicalize([]byte(`{"a":1,"a":2}`))
	if err == nil {
		t.Fatal("expected error for duplicate object key, got nil")
	}
}

func TestCanonicalize_NestedDuplicateKeyRejected(t *testing.T) {
	_, err := Canonicalize([]byte(`{"a":{"x":1,"x":2}}`))
	if err == nil {
		t.Fatal("expected error for nested duplicate object key, got nil")
	}
}

func TestCanonicalize_InvalidUTF8Rejected(t *testing.T) {
	_, err := Canonicalize([]byte("{\"a\":\"\xff\xfe\"}"))
	if err == nil {
		t.Fatal("expected error for invalid UTF-8, got nil")
	}
}

func TestCanonicalize_TrailingDataRejected(t *testing.T) {
	_, err := Canonicalize([]byte(`{"a":1} garbage`))
	if err == nil {
		t.Fatal("expected error for trailing data, got nil")
	}
}

// TestCanonicalize_UTF16SurrogateOrdering asserts the well-known RFC 8785
// quirk: object keys are sorted by UTF-16 *code unit* value, so a
// supplementary-plane character (encoded as a surrogate pair starting at
// 0xD800-0xDBFF) sorts before a BMP character in 0xE000-0xFFFF, even though
// its Unicode code point is numerically larger.
func TestCanonicalize_UTF16SurrogateOrdering(t *testing.T) {
	// "\U0001F600" (😀) encodes as UTF-16 surrogate pair starting 0xD83D.
	// "￿" is a plain BMP code unit 0xFFFF. 0xD83D < 0xFFFF, so the
	// emoji key must sort first even though U+1F600 > U+FFFF as a code point.
	in := []byte("{\"￿\":1,\"\U0001F600\":2}")
	want := "{\"\U0001F600\":2,\"￿\":1}"
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalize_StringEscaping(t *testing.T) {
	// Only '"', '\\', and control characters are escaped; '/' and non-ASCII
	// characters are emitted raw.
	// The JSON source escapes the control character (a raw control byte
	// inside a JSON string is invalid per RFC 8259).
	in := []byte("{\"s\":\"a/b\\\"c\\\\d\\u0007e€\"}")
	want := "{\"s\":\"a/b\\\"c\\\\d\\u0007e€\"}"
	got, err := Canonicalize(in)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestFormatNumber checks the ECMAScript Number::toString algorithm against
// well-established JavaScript Number.prototype.toString() outputs.
func TestFormatNumber(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{100, "100"},
		{1.5, "1.5"},
		{4.5, "4.5"},
		{2e-3, "0.002"},
		{0.000001, "0.000001"}, // 1e-6, still fixed notation
		{0.0000001, "1e-7"},    // 1e-7, exponential
		{1e20, "100000000000000000000"},
		{1e21, "1e+21"},
		{-1e21, "-1e+21"},
		{123456789, "123456789"},
		{-42.5, "-42.5"},
	}
	for _, c := range cases {
		got, err := formatNumber(c.in)
		if err != nil {
			t.Fatalf("formatNumber(%v): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("formatNumber(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatNumber_NegativeZero(t *testing.T) {
	got, err := formatNumber(-0.0)
	if err != nil {
		t.Fatalf("formatNumber(-0): %v", err)
	}
	if got != "0" {
		t.Fatalf("formatNumber(-0) = %q, want %q", got, "0")
	}
}

func TestParseWithLimits_MaxDepth(t *testing.T) {
	_, err := ParseWithLimits([]byte(`{"a":{"b":{"c":1}}}`), Limits{MaxDepth: 2})
	if err == nil {
		t.Fatal("expected depth-limit error, got nil")
	}
}

func TestParseWithLimits_MaxStringLen(t *testing.T) {
	_, err := ParseWithLimits([]byte(`{"a":"toolong"}`), Limits{MaxStringLen: 3})
	if err == nil {
		t.Fatal("expected string-length-limit error, got nil")
	}
}

func TestParseWithLimits_MaxArrayLen(t *testing.T) {
	_, err := ParseWithLimits([]byte(`[1,2,3]`), Limits{MaxArrayLen: 2})
	if err == nil {
		t.Fatal("expected array-length-limit error, got nil")
	}
}

func TestParseWithLimits_MaxObjectKeys(t *testing.T) {
	_, err := ParseWithLimits([]byte(`{"a":1,"b":2,"c":3}`), Limits{MaxObjectKeys: 2})
	if err == nil {
		t.Fatal("expected object-key-limit error, got nil")
	}
}

func TestParseWithLimits_MaxBytes(t *testing.T) {
	_, err := ParseWithLimits([]byte(`{"a":1}`), Limits{MaxBytes: 3})
	if err == nil {
		t.Fatal("expected max-bytes error, got nil")
	}
}
