package assets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rwa-platform/server/internal/auditpkg"
)

// TestDialectLimitsMatchFrozenConstants pins the shared structural-limit
// contract: shared/schemas/structural-limits.json (mirrored into the vectors
// file's "limits" object) MUST equal the constants both the server (auditpkg)
// and the offline signer hardcode. If the two ever drift — say one side keeps
// depth 32 while the other bumps to 64 — a document one accepts the other
// rejects, so this test fails loudly instead.
func TestDialectLimitsMatchFrozenConstants(t *testing.T) {
	v := loadConformanceVectors(t)
	got := [5]int{auditpkg.MaxDocumentBytes, auditpkg.MaxObjectDepth, auditpkg.MaxStringLength, auditpkg.MaxArrayLength, auditpkg.MaxObjectKeys}
	want := [5]int{v.Limits.MaxUncompressedBytes, v.Limits.MaxDepth, v.Limits.MaxStringLength, v.Limits.MaxArrayItems, v.Limits.MaxObjectProperties}
	if got != want {
		t.Fatalf("server auditpkg limits %v do not match frozen shared/schemas/structural-limits.json %v", got, want)
	}
}

// TestDialectConformanceInstances is the instance-validation gate: a schema
// compiling is not enough — the two validators must agree on whether a given
// instance satisfies it. Complements TestDialectConformance (which only checks
// compile/reject).
func TestDialectConformanceInstances(t *testing.T) {
	v := loadConformanceVectors(t)
	if len(v.InstanceCases) == 0 {
		t.Fatal("shared/vectors/schema-dialect-conformance.json has no instanceCases")
	}
	for _, c := range v.InstanceCases {
		t.Run(c.Name, func(t *testing.T) {
			if err := guardAssetSchema(c.Schema); err != nil {
				t.Fatalf("schema unexpectedly rejected by guard: %v", err)
			}
			compiled, err := compileAssetSchema(c.Schema)
			if err != nil {
				t.Fatalf("schema unexpectedly failed to compile: %v", err)
			}
			var instance any
			if err := json.Unmarshal(c.Instance, &instance); err != nil {
				t.Fatalf("vector instance is not valid JSON: %v", err)
			}
			valid := compiled.Validate(instance) == nil
			switch c.ExpectValid {
			case "valid":
				if !valid {
					t.Fatalf("expected instance to validate against schema, got rejected")
				}
			case "invalid":
				if valid {
					t.Fatalf("expected instance to be rejected by schema, got accepted")
				}
			default:
				t.Fatalf("unknown expectValid %q", c.ExpectValid)
			}
		})
	}
}

// TestDialectConformanceEnvelope runs every envelopeCases document through
// the real ValidateProfile/ValidateRecord entry points — not just the bare
// schema guard/compiler — so a nested-envelope strictness gap (e.g. an extra
// field slipping through displayFields[]) is caught the same way a real
// request would hit it.
func TestDialectConformanceEnvelope(t *testing.T) {
	v := loadConformanceVectors(t)
	if len(v.EnvelopeCases) == 0 {
		t.Fatal("shared/vectors/schema-dialect-conformance.json has no envelopeCases")
	}
	for _, c := range v.EnvelopeCases {
		t.Run(c.Name, func(t *testing.T) {
			var result ValidationResult
			switch c.Target {
			case "profile":
				result, _ = ValidateProfile(c.Document)
			case "metadata":
				result, _ = ValidateRecord(nil, c.Document, "", "")
			default:
				t.Fatalf("unknown envelope target %q", c.Target)
			}
			switch c.ExpectValid {
			case "valid":
				if !result.Valid {
					t.Fatalf("expected %s to validate, got errors: %v (%s)", c.Target, result.Errors, c.Note)
				}
			case "invalid":
				if result.Valid {
					t.Fatalf("expected %s to be rejected, got valid (%s)", c.Target, c.Note)
				}
			default:
				t.Fatalf("unknown expectValid %q", c.ExpectValid)
			}
		})
	}
}

// TestDialectConformanceLimitBoundaries builds, for every limitCases entry,
// a document sitting exactly AT its declared limit (must be accepted) and
// exactly one unit past it (must be rejected), and runs both through
// auditpkg.CheckLimits — the same choke point ValidateProfile/ValidateRecord
// call before any recursive parsing (see profile.go/record.go). This is the
// server side of the exact-boundary check for every structural limit; the
// signer runs the identical construction against its own limit checker.
func TestDialectConformanceLimitBoundaries(t *testing.T) {
	v := loadConformanceVectors(t)
	if len(v.LimitCases) == 0 {
		t.Fatal("shared/vectors/schema-dialect-conformance.json has no limitCases")
	}
	for _, c := range v.LimitCases {
		t.Run(c.Name, func(t *testing.T) {
			atLimit, overLimit, err := buildLimitBoundary(c.Kind, c.Limit)
			if err != nil {
				t.Fatalf("building boundary documents for kind %q: %v", c.Kind, err)
			}
			if err := auditpkg.CheckLimits(atLimit); err != nil {
				t.Fatalf("document exactly at the %s limit (%d) was rejected: %v", c.Kind, c.Limit, err)
			}
			if err := auditpkg.CheckLimits(overLimit); err == nil {
				t.Fatalf("document one unit past the %s limit (%d) was accepted", c.Kind, c.Limit)
			}
		})
	}
}

// buildLimitBoundary constructs two JSON documents for structural-limit kind
// — one exactly at limit, one exactly limit+1 — using a shape that doesn't
// itself trip any OTHER structural limit before the one under test (e.g.
// the "bytes" case pads with insignificant whitespace rather than a long
// string, since a >32768-char string would trip MaxStringLength first).
func buildLimitBoundary(kind string, limit int) (atLimit, overLimit []byte, err error) {
	switch kind {
	case "string":
		return []byte(`{"a":"` + strings.Repeat("x", limit) + `"}`), []byte(`{"a":"` + strings.Repeat("x", limit+1) + `"}`), nil
	case "string-2byte":
		return jsonValueUTF8Bytes('é', limit), jsonValueUTF8Bytes('é', limit+1), nil
	case "string-3byte":
		return jsonValueUTF8Bytes('中', limit), jsonValueUTF8Bytes('中', limit+1), nil
	case "string-4byte":
		return jsonValueUTF8Bytes('\U0001F600', limit), jsonValueUTF8Bytes('\U0001F600', limit+1), nil
	case "object-key":
		return jsonKeyUTF8Bytes('k', limit), jsonKeyUTF8Bytes('k', limit+1), nil
	case "object-key-4byte":
		return jsonKeyUTF8Bytes('\U0001F600', limit), jsonKeyUTF8Bytes('\U0001F600', limit+1), nil
	case "array":
		return []byte(`{"a":[` + repeatCSV("0", limit) + `]}`), []byte(`{"a":[` + repeatCSV("0", limit+1) + `]}`), nil
	case "object":
		return buildObjectWithNProps(limit), buildObjectWithNProps(limit + 1), nil
	case "depth":
		return []byte(strings.Repeat("[", limit) + strings.Repeat("]", limit)), []byte(strings.Repeat("[", limit+1) + strings.Repeat("]", limit+1)), nil
	case "bytes":
		// "{" + N spaces + "}" is valid (if odd) JSON — an empty object with
		// insignificant interior whitespace — and has exactly N+2 bytes.
		if limit < 2 {
			return nil, nil, fmt.Errorf("bytes limit %d too small for the {<pad>} construction", limit)
		}
		return []byte("{" + strings.Repeat(" ", limit-2) + "}"), []byte("{" + strings.Repeat(" ", limit-1) + "}"), nil
	default:
		return nil, nil, fmt.Errorf("unknown limit kind %q", kind)
	}
}

// utf8BytesOfLength returns a string whose UTF-8 encoding is exactly
// totalBytes long, built from as many whole copies of r as fit and padded
// with single-byte ASCII filler to hit the exact byte count -- so its rune
// count is far smaller than totalBytes whenever r is multi-byte. Used by
// the string-2byte/-3byte/-4byte and object-key-*byte limitCase kinds to pin
// the length bound as UTF-8 BYTES, not runes, matching
// signer/internal/jsonschema/dialect_conformance_test.go's identical helper.
func utf8BytesOfLength(r rune, totalBytes int) string {
	width := utf8.RuneLen(r)
	var b strings.Builder
	for b.Len()+width <= totalBytes {
		b.WriteRune(r)
	}
	for b.Len() < totalBytes {
		b.WriteByte('x')
	}
	return b.String()
}

// jsonValueUTF8Bytes returns {"a":"<value>"} where the value's UTF-8-encoded
// content is exactly totalBytes long, built from rune r.
func jsonValueUTF8Bytes(r rune, totalBytes int) []byte {
	val, _ := json.Marshal(utf8BytesOfLength(r, totalBytes))
	return []byte(`{"a":` + string(val) + `}`)
}

// jsonKeyUTF8Bytes returns a single-property JSON object whose key's
// UTF-8-encoded length is exactly totalBytes, built from rune r.
func jsonKeyUTF8Bytes(r rune, totalBytes int) []byte {
	key, _ := json.Marshal(utf8BytesOfLength(r, totalBytes))
	return []byte(`{` + string(key) + `:0}`)
}

func repeatCSV(elem string, n int) string {
	elems := make([]string, n)
	for i := range elems {
		elems[i] = elem
	}
	return strings.Join(elems, ",")
}

func buildObjectWithNProps(n int) []byte {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":0`, i)
	}
	b.WriteByte('}')
	return []byte(b.String())
}
