// Package canonical implements RFC 8785 JSON Canonicalization Scheme (JCS).
//
// It provides a strict JSON parser (duplicate object keys are rejected,
// input must be valid UTF-8) and a canonical serializer (object keys sorted
// by UTF-16 code unit, ECMAScript Number::toString number formatting,
// minimal string escaping) so that Go and any other JCS-compliant
// implementation (the server's TypeScript canonicalizer, in particular)
// produce byte-identical output for the same logical JSON document.
package canonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Value is a parsed JSON value: nil, bool, json.Number, string, []Value, or *Object.
type Value interface{}

// Object is a JSON object that preserves insertion order and rejects
// duplicate keys during parsing.
type Object struct {
	keys   []string
	values map[string]Value
}

// Keys returns the object's keys in the order they were parsed.
func (o *Object) Keys() []string {
	return o.keys
}

// Get returns the value for key and whether it was present.
func (o *Object) Get(key string) (Value, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Len returns the number of properties in the object.
func (o *Object) Len() int {
	return len(o.keys)
}

func newObject() *Object {
	return &Object{values: make(map[string]Value)}
}

func (o *Object) set(key string, v Value) error {
	if _, exists := o.values[key]; exists {
		return fmt.Errorf("canonical: duplicate object key %q", key)
	}
	o.keys = append(o.keys, key)
	o.values[key] = v
	return nil
}

// Limits bounds the shape of a document during ParseWithLimits, guarding
// against pathological or malicious input before it is ever hashed or
// walked by a schema validator.
type Limits struct {
	MaxBytes int64 // total input size in bytes; 0 means unbounded
	MaxDepth int   // maximum object/array nesting depth; 0 means unbounded
	// MaxStringLen is the maximum length, in UTF-8 BYTES (Go len(string)),
	// of any JSON string token -- applied IDENTICALLY to string values AND
	// object keys. The unit is UTF-8 bytes to match the server's limits.go,
	// which measures every string token (key or value) with len(string). The
	// unit matters: measuring runes for values only, and skipping keys, would
	// let a 10,000-emoji value or a 32,769-byte object key slip past here
	// while the server rejects it. 0 means unbounded.
	MaxStringLen  int
	MaxArrayLen   int // maximum number of elements in any array; 0 means unbounded
	MaxObjectKeys int // maximum number of properties in any object; 0 means unbounded
}

// DefaultLimits returns the structural limits for operator-supplied
// profile and metadata documents. These are the exact values the server
// enforces (server/internal/auditpkg/canonical.go: MaxDocumentBytes,
// MaxObjectDepth, MaxStringLength, MaxArrayLength, MaxObjectKeys). They must
// stay in lockstep: if the signer's limits are looser than the server's, a
// hand-built package can pass here while the server rejects it, so the two
// validators would disagree on the same document. Both sides MUST use these
// same numbers; do not change them here without also changing the server's
// copy and shared/vectors/schema-dialect-conformance.json's boundary cases.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:      1 << 20, // 1 MiB
		MaxDepth:      32,
		MaxStringLen:  32768,
		MaxArrayLen:   4096,
		MaxObjectKeys: 4096,
	}
}

type parser struct {
	dec   *json.Decoder
	limit Limits
}

// Parse parses input into a Value tree with no size/depth limits beyond a
// hard-coded safety ceiling, rejecting duplicate object keys, invalid
// UTF-8, and trailing data. Use ParseWithLimits to enforce document limits.
func Parse(input []byte) (Value, error) {
	return ParseWithLimits(input, Limits{})
}

// ParseWithLimits parses input into a Value tree, enforcing lim (zero
// fields are treated as unbounded, except depth which always has a hard
// safety ceiling to protect the Go call stack).
func ParseWithLimits(input []byte, lim Limits) (Value, error) {
	if lim.MaxBytes > 0 && int64(len(input)) > lim.MaxBytes {
		return nil, fmt.Errorf("canonical: document exceeds max size of %d bytes", lim.MaxBytes)
	}
	if !utf8.Valid(input) {
		return nil, errors.New("canonical: input is not valid UTF-8")
	}

	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	p := &parser{dec: dec, limit: lim}

	v, err := p.parseValue(0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("canonical: trailing data after top-level JSON value")
		}
		return nil, fmt.Errorf("canonical: trailing data after top-level JSON value: %w", err)
	}
	return v, nil
}

const hardMaxDepth = 512

func (p *parser) parseValue(depth int) (Value, error) {
	if depth > hardMaxDepth || (p.limit.MaxDepth > 0 && depth > p.limit.MaxDepth) {
		return nil, errors.New("canonical: document exceeds max nesting depth")
	}
	tok, err := p.dec.Token()
	if err != nil {
		return nil, fmt.Errorf("canonical: %w", err)
	}
	return p.parseTokenValue(tok, depth)
}

func (p *parser) parseTokenValue(tok json.Token, depth int) (Value, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return p.parseObject(depth)
		case '[':
			return p.parseArray(depth)
		default:
			return nil, fmt.Errorf("canonical: unexpected token %q", t)
		}
	case nil:
		return nil, nil
	case bool:
		return t, nil
	case json.Number:
		return t, nil
	case string:
		// Byte length, not rune count -- must match the server's Go
		// len(string) check byte-for-byte. See Limits.MaxStringLen.
		if p.limit.MaxStringLen > 0 && len(t) > p.limit.MaxStringLen {
			return nil, fmt.Errorf("canonical: string exceeds max length of %d bytes", p.limit.MaxStringLen)
		}
		return t, nil
	default:
		return nil, fmt.Errorf("canonical: unexpected token type %T", tok)
	}
}

func (p *parser) parseObject(depth int) (*Object, error) {
	obj := newObject()
	for p.dec.More() {
		if p.limit.MaxObjectKeys > 0 && obj.Len() >= p.limit.MaxObjectKeys {
			return nil, fmt.Errorf("canonical: object exceeds max property count of %d", p.limit.MaxObjectKeys)
		}
		keyTok, err := p.dec.Token()
		if err != nil {
			return nil, fmt.Errorf("canonical: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("canonical: expected object key, got %T", keyTok)
		}
		// Object keys are bound by the same MaxStringLen (in UTF-8 bytes) as
		// string values, matching the server's limits.go, which enforces the
		// bound on every string token including keys.
		if p.limit.MaxStringLen > 0 && len(key) > p.limit.MaxStringLen {
			return nil, fmt.Errorf("canonical: object key exceeds max length of %d bytes", p.limit.MaxStringLen)
		}
		val, err := p.parseValue(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := obj.set(key, val); err != nil {
			return nil, err
		}
	}
	// consume closing '}'
	if _, err := p.dec.Token(); err != nil {
		return nil, fmt.Errorf("canonical: %w", err)
	}
	return obj, nil
}

func (p *parser) parseArray(depth int) ([]Value, error) {
	arr := []Value{}
	for p.dec.More() {
		if p.limit.MaxArrayLen > 0 && len(arr) >= p.limit.MaxArrayLen {
			return nil, fmt.Errorf("canonical: array exceeds max length of %d", p.limit.MaxArrayLen)
		}
		v, err := p.parseValue(depth + 1)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
	}
	// consume closing ']'
	if _, err := p.dec.Token(); err != nil {
		return nil, fmt.Errorf("canonical: %w", err)
	}
	return arr, nil
}

// Canonicalize parses input as JSON and re-serializes it as RFC 8785
// canonical bytes. It rejects duplicate object keys, invalid UTF-8, and
// non-finite numbers.
func Canonicalize(input []byte) ([]byte, error) {
	v, err := Parse(input)
	if err != nil {
		return nil, err
	}
	return Marshal(v)
}

// Marshal serializes an already-parsed Value tree as RFC 8785 canonical bytes.
func Marshal(v Value) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeValue(buf *bytes.Buffer, v Value) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return fmt.Errorf("canonical: invalid number %q: %w", t.String(), err)
		}
		s, err := formatNumber(f)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case string:
		writeString(buf, t)
	case []Value:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeValue(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case *Object:
		buf.WriteByte('{')
		keys := append([]string(nil), t.keys...)
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			val, _ := t.Get(k)
			if err := writeValue(buf, val); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical: unsupported value type %T", v)
	}
	return nil
}

// utf16Less reports whether a sorts before b when both are compared as
// sequences of UTF-16 code units, per RFC 8785 section 3.2.3.
func utf16Less(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	n := len(ua)
	if len(ub) < n {
		n = len(ub)
	}
	for i := 0; i < n; i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeString writes s as a JSON string using the minimal escaping RFC 8785
// requires: only '"', '\\', and control characters (U+0000-U+001F) are
// escaped; everything else, including non-ASCII characters and '/', is
// emitted as raw UTF-8.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// formatNumber renders f using the ECMAScript Number::toString algorithm
// (ECMA-262 7.1.12.1), which RFC 8785 mandates for JSON number
// serialization.
func formatNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("canonical: NaN/Infinity cannot be canonicalized")
	}
	if f == 0 {
		// Both +0 and -0 serialize as "0".
		return "0", nil
	}

	neg := f < 0
	af := math.Abs(f)

	// Shortest round-tripping decimal digits, in scientific notation:
	// af == digits * 10^(exp - (len(digits)-1))
	repr := strconv.FormatFloat(af, 'e', -1, 64)
	mantissa, expPart, found := strings.Cut(repr, "e")
	if !found {
		return "", fmt.Errorf("canonical: unexpected float format %q", repr)
	}
	exp, err := strconv.Atoi(expPart)
	if err != nil {
		return "", fmt.Errorf("canonical: unexpected exponent %q: %w", expPart, err)
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	k := len(digits)
	n := exp + 1 // decimal point position per ECMA-262 7.1.12.1

	var sb strings.Builder
	switch {
	case k <= n && n <= 21:
		sb.WriteString(digits)
		for i := 0; i < n-k; i++ {
			sb.WriteByte('0')
		}
	case 0 < n && n <= 21:
		sb.WriteString(digits[:n])
		sb.WriteByte('.')
		sb.WriteString(digits[n:])
	case -6 < n && n <= 0:
		sb.WriteString("0.")
		for i := 0; i < -n; i++ {
			sb.WriteByte('0')
		}
		sb.WriteString(digits)
	default:
		sb.WriteByte(digits[0])
		if k > 1 {
			sb.WriteByte('.')
			sb.WriteString(digits[1:])
		}
		sb.WriteByte('e')
		e := n - 1
		if e >= 0 {
			sb.WriteByte('+')
		} else {
			sb.WriteByte('-')
			e = -e
		}
		sb.WriteString(strconv.Itoa(e))
	}

	if neg {
		return "-" + sb.String(), nil
	}
	return sb.String(), nil
}
