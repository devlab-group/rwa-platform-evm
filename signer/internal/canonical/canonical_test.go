package canonical

import (
	"math"
	"strings"
	"testing"
)

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

// Require UTF-16 ordering: leading surrogates 0xD800-0xDBFF precede BMP 0xE000-0xFFFF
// despite larger code points
func TestCanonicalize_UTF16SurrogateOrdering(t *testing.T) {
	// U+1F600 starts with surrogate 0xD83D, before BMP U+FFFF's 0xFFFF; the emoji key
	// sorts first despite its larger code point
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
	// Escape quotes, backslashes and controls; emit '/' and non-ASCII raw. The JSON source
	// must escape controls because raw control bytes are invalid (RFC 8259).
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

// Compare ECMAScript number formatting with known Number.prototype.toString() outputs
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
	// A literal -0.0 is folded to +0 by Go, so build the sign bit explicitly
	negZero := math.Copysign(0, -1)
	if !math.Signbit(negZero) {
		t.Fatal("test setup: expected a negative zero")
	}
	got, err := formatNumber(negZero)
	if err != nil {
		t.Fatalf("formatNumber(-0): %v", err)
	}
	if got != "0" {
		t.Fatalf("formatNumber(-0) = %q, want %q", got, "0")
	}
}

// JSON text -0 must canonicalize to 0, alone and nested
func TestCanonicalize_NegativeZeroText(t *testing.T) {
	for in, want := range map[string]string{"-0": "0", "[-0]": "[0]", `{"a":-0.0}`: `{"a":0}`} {
		got, err := Canonicalize([]byte(in))
		if err != nil {
			t.Fatalf("Canonicalize(%q): %v", in, err)
		}
		if string(got) != want {
			t.Errorf("Canonicalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzCanonicalize(f *testing.F) {
	for _, seed := range []string{
		`{"b":1,"a":2,"c":{"z":1,"y":[true,null,"\u00e9"]}}`,
		`[-0,1.5e3,1e21,0.000001,9007199254740991]`,
		`"\ud83d\ude00"`,
		`{"a":1,"a":2}`,
		``,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := Canonicalize(in)
		if err != nil {
			return
		}
		again, err := Canonicalize(out)
		if err != nil {
			t.Fatalf("canonical output %q does not re-parse: %v", out, err)
		}
		if string(again) != string(out) {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
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

// RFC 8785 section 3.2.2.2: only quote, backslash and U+0000-U+001F are escaped, the
// short forms use their letters and the rest use lower-case \u00xx; everything else,
// including DEL, U+2028 and '/', stays raw
func TestCanonicalize_StringEscapeTable(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"quote", `"\""`, `"\""`},
		{"backslash", `"\\"`, `"\\"`},
		{"backspace", `"\b"`, `"\b"`},
		{"form feed", `"\f"`, `"\f"`},
		{"line feed", `"\n"`, `"\n"`},
		{"carriage return", `"\r"`, `"\r"`},
		{"tab", `"\t"`, `"\t"`},
		{"NUL", `"\u0000"`, `"\u0000"`},
		{"U+000b", `"\u000B"`, `"\u000b"`},
		{"U+001f upper-case input", `"\u001F"`, `"\u001f"`},
		{"escaped solidus is emitted raw", `"\/"`, `"/"`},
		{"DEL stays raw", `"\u007f"`, "\"\u007f\""},
		{"U+2028 stays raw", `" "`, "\" \""},
		{"escaped quote in a key", `{"a\"b":1}`, `{"a\"b":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tt.in))
			if err != nil {
				t.Fatalf("Canonicalize(%s): %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("Canonicalize(%s) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// DefaultLimits enables SafeIntegers, matching the server: reject integers outside
// [-(2^53-1), 2^53-1], except literals containing a fraction or exponent
func TestParseWithLimits_SafeIntegers(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{`9007199254740991`, false},
		{`-9007199254740991`, false},
		{`9007199254740992`, true},
		{`-9007199254740992`, true},
		{`[1, 123456789012345678901234567890]`, true},
		{`9007199254740993.0`, false},
		{`9007199254740992e0`, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			for name, lim := range map[string]Limits{"explicit": {SafeIntegers: true}, "DefaultLimits": DefaultLimits()} {
				_, err := ParseWithLimits([]byte(tt.in), lim)
				if (err != nil) != tt.wantErr {
					t.Errorf("%s: ParseWithLimits(%s) error = %v, wantErr %v", name, tt.in, err, tt.wantErr)
				}
				if err != nil && !strings.Contains(err.Error(), "outside the safe range") {
					t.Errorf("%s: error = %v, want the safe-range message", name, err)
				}
			}
			if _, err := ParseWithLimits([]byte(tt.in), Limits{}); err != nil {
				t.Errorf("without SafeIntegers, ParseWithLimits(%s) = %v, want success", tt.in, err)
			}
		})
	}
}
