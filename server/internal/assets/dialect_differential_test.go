package assets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rwa-platform/server/internal/auditpkg"
)

// TestDialectLimitsMatchFrozenConstants compares auditpkg limits with the conformance
// vectors' frozen limits, also checked by the offline signer, so both enforce the same
// bounds
func TestDialectLimitsMatchFrozenConstants(t *testing.T) {
	v := loadConformanceVectors(t)
	got := [5]int{auditpkg.MaxDocumentBytes, auditpkg.MaxObjectDepth, auditpkg.MaxStringLength, auditpkg.MaxArrayLength, auditpkg.MaxObjectKeys}
	want := [5]int{v.Limits.MaxUncompressedBytes, v.Limits.MaxDepth, v.Limits.MaxStringLength, v.Limits.MaxArrayItems, v.Limits.MaxObjectProperties}
	if got != want {
		t.Fatalf("server auditpkg limits %v do not match frozen shared/schemas/structural-limits.json %v", got, want)
	}
}

// TestDialectLimitsCopyMatchesStructuralLimits checks the vectors' limits against
// shared/schemas/structural-limits.json, preventing an outdated copy from hiding
// changes from either binary's tests
func TestDialectLimitsCopyMatchesStructuralLimits(t *testing.T) {
	v := loadConformanceVectors(t)
	p := filepath.Join("..", "..", "..", "shared", "schemas", "structural-limits.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	var frozen conformanceVectors
	if err := json.Unmarshal(raw, &frozen.Limits); err != nil {
		t.Fatalf("parsing %s: %v", p, err)
	}
	if frozen.Limits != v.Limits {
		t.Fatalf("vectors limits %+v do not match %s %+v", v.Limits, p, frozen.Limits)
	}
}

// TestDialectConformanceInstances checks instance validation parity, beyond the schema
// compilation and rejection verdicts in TestDialectConformance
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

// TestDialectConformanceEnvelope sends envelopeCases through ValidateProfile and
// ValidateRecord to catch nested unknown fields, such as an extra displayFields[]
// member, beyond schema guard and compiler checks
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

// TestDialectConformanceLimitBoundaries sends each limitCases boundary and boundary+1
// through auditpkg.CheckLimits: accept the former, reject the latter. ValidateProfile
// and ValidateRecord use this pre-parse check; the signer tests equivalent documents.
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

// buildLimitBoundary isolates each structural limit with documents at limit and
// limit+1. The bytes case pads with whitespace; a string would hit MaxStringLength
// (32768 bytes) first.
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
		// "{", N spaces and "}" is an empty object of exactly N+2 bytes
		if limit < 2 {
			return nil, nil, fmt.Errorf("bytes limit %d too small for the {<pad>} construction", limit)
		}
		return []byte("{" + strings.Repeat(" ", limit-2) + "}"), []byte("{" + strings.Repeat(" ", limit-1) + "}"), nil
	default:
		return nil, nil, fmt.Errorf("unknown limit kind %q", kind)
	}
}

// utf8BytesOfLength fills totalBytes with r and ASCII padding, distinguishing byte
// limits from rune counts for strings and keys. The signer uses the same helper in
// dialect_conformance_test.go.
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

// jsonValueUTF8Bytes returns {"a":"<value>"} where the value is totalBytes UTF-8
// bytes long, built from rune r
func jsonValueUTF8Bytes(r rune, totalBytes int) []byte {
	val, _ := json.Marshal(utf8BytesOfLength(r, totalBytes))
	return []byte(`{"a":` + string(val) + `}`)
}

// jsonKeyUTF8Bytes returns a one-property JSON object whose key is totalBytes
// UTF-8 bytes long, built from rune r
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
