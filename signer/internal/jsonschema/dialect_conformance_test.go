package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rwa-platform/signer/internal/canonical"
)

// conformanceCase mirrors one entry of shared/vectors/schema-dialect-conformance.json's
// top-level "cases" array: whether the schema compiles
type conformanceCase struct {
	Name   string          `json:"name"`
	Expect string          `json:"expect"` // "accept" | "reject"
	Note   string          `json:"note"`
	Schema json.RawMessage `json:"schema"`
}

// instanceCase mirrors one entry of "instanceCases": the schema compiles, and both
// validators must agree on whether instance validates against it
type instanceCase struct {
	Name        string          `json:"name"`
	Schema      json.RawMessage `json:"schema"`
	Instance    json.RawMessage `json:"instance"`
	ExpectValid string          `json:"expectValid"` // "valid" | "invalid"
	Note        string          `json:"note"`
}

// limitCase mirrors one entry of "limitCases": a structural limit dimension (kind) and
// the boundary value both binaries must enforce
type limitCase struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // "string" | "string-2byte" | "string-3byte" | "string-4byte" | "array" | "object" | "object-key" | "object-key-4byte" | "depth" | "bytes"
	Limit int    `json:"limit"`
	Note  string `json:"note"`
}

// sharedLimits mirrors the "limits" object of schema-dialect-conformance.json, a copy of
// shared/schemas/structural-limits.json
type sharedLimits struct {
	MaxUncompressedBytes int `json:"maxUncompressedBytes"`
	MaxDepth             int `json:"maxDepth"`
	MaxStringLength      int `json:"maxStringLength"`
	MaxArrayItems        int `json:"maxArrayItems"`
	MaxObjectProperties  int `json:"maxObjectProperties"`
}

type conformanceFile struct {
	Dialect       string            `json:"dialect"`
	Cases         []conformanceCase `json:"cases"`
	InstanceCases []instanceCase    `json:"instanceCases"`
	LimitCases    []limitCase       `json:"limitCases"`
	Limits        sharedLimits      `json:"limits"`
}

// loadConformanceFile reads and parses shared/vectors/schema-dialect-conformance.json
// and fails the test if it is missing. The file is a shared contract
// (docs/spec/profile-schema-dialect.md), not something the signer generates.
func loadConformanceFile(t *testing.T) conformanceFile {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "shared", "vectors", "schema-dialect-conformance.json"))
	if err != nil {
		t.Fatalf("resolving shared/vectors/schema-dialect-conformance.json path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading shared/vectors/schema-dialect-conformance.json: %v", err)
	}
	var file conformanceFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing conformance vectors: %v", err)
	}
	if file.Dialect != "rwa-profile-assetSchema/1" {
		t.Fatalf("unexpected dialect id %q", file.Dialect)
	}
	return file
}

// TestDialectConformance reproduces the "cases" array of
// shared/vectors/schema-dialect-conformance.json: Compile must accept or reject each
// schema as the case's "expect" says. The signer and server compilers must agree on
// every case. The test name matters: `make dialect-check` runs
// `go test ./internal/jsonschema/... ./internal/profile/... -run Dialect -count=1`,
// which matches every test whose name contains "Dialect", and the server follows the
// same convention.
func TestDialectConformance(t *testing.T) {
	file := loadConformanceFile(t)
	if len(file.Cases) == 0 {
		t.Fatal("conformance vector file has no cases")
	}

	for _, c := range file.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			_, err := CompileBytes(c.Schema)
			switch c.Expect {
			case "accept":
				if err != nil {
					t.Errorf("case %q: expected accept, got reject: %v", c.Name, err)
				}
			case "reject":
				if err == nil {
					t.Errorf("case %q: expected reject, got accept", c.Name)
				}
			default:
				t.Fatalf("case %q: unknown expect value %q", c.Name, c.Expect)
			}
		})
	}
}

// TestDialectInstanceValidation reproduces "instanceCases". Compile-only parity is not
// enough: an assetSchema exists to validate the asset a signature attests to, so the
// two validators must also agree on every accept or reject verdict for actual data.
func TestDialectInstanceValidation(t *testing.T) {
	file := loadConformanceFile(t)
	if len(file.InstanceCases) == 0 {
		t.Fatal("conformance vector file has no instanceCases")
	}

	for _, c := range file.InstanceCases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			schema, err := CompileBytes(c.Schema)
			if err != nil {
				t.Fatalf("case %q: schema unexpectedly failed to compile: %v", c.Name, err)
			}
			err = schema.ValidateBytes(c.Instance)
			switch c.ExpectValid {
			case "valid":
				if err != nil {
					t.Errorf("case %q: expected instance to validate, got: %v", c.Name, err)
				}
			case "invalid":
				if err == nil {
					t.Errorf("case %q: expected instance to be rejected, but it validated", c.Name)
				}
			default:
				t.Fatalf("case %q: unknown expectValid value %q", c.Name, c.ExpectValid)
			}
		})
	}
}

// TestDialectStructuralLimitsMatchSharedConstants asserts that canonical.DefaultLimits()
// equals the "limits" object of shared/vectors/schema-dialect-conformance.json (a copy
// of shared/schemas/structural-limits.json) field for field. Divergent limits would let
// a hand-built package stay under one side's limits while exceeding the other's.
func TestDialectStructuralLimitsMatchSharedConstants(t *testing.T) {
	file := loadConformanceFile(t)
	want := file.Limits
	got := canonical.DefaultLimits()

	if int64(want.MaxUncompressedBytes) != got.MaxBytes {
		t.Errorf("MaxBytes = %d, shared limits say %d", got.MaxBytes, want.MaxUncompressedBytes)
	}
	if want.MaxDepth != got.MaxDepth {
		t.Errorf("MaxDepth = %d, shared limits say %d", got.MaxDepth, want.MaxDepth)
	}
	if want.MaxStringLength != got.MaxStringLen {
		t.Errorf("MaxStringLen = %d, shared limits say %d", got.MaxStringLen, want.MaxStringLength)
	}
	if want.MaxArrayItems != got.MaxArrayLen {
		t.Errorf("MaxArrayLen = %d, shared limits say %d", got.MaxArrayLen, want.MaxArrayItems)
	}
	if want.MaxObjectProperties != got.MaxObjectKeys {
		t.Errorf("MaxObjectKeys = %d, shared limits say %d", got.MaxObjectKeys, want.MaxObjectProperties)
	}
}

// TestDialectStructuralLimits checks acceptance at each limit and rejection at
// limit+1 from limitCases. Each test enables only the limit under examination;
// for example, a roughly 1 MiB byte-limit input must not fail a string, array
// or property-count limit first.
func TestDialectStructuralLimits(t *testing.T) {
	file := loadConformanceFile(t)
	if len(file.LimitCases) == 0 {
		t.Fatal("conformance vector file has no limitCases")
	}

	for _, lc := range file.LimitCases {
		lc := lc
		t.Run(lc.Name, func(t *testing.T) {
			switch lc.Kind {
			case "string":
				lim := canonical.Limits{MaxStringLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonString(lc.Limit), jsonString(lc.Limit+1))
			case "string-2byte":
				lim := canonical.Limits{MaxStringLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonStringUTF8Bytes('é', lc.Limit), jsonStringUTF8Bytes('é', lc.Limit+1))
			case "string-3byte":
				lim := canonical.Limits{MaxStringLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonStringUTF8Bytes('中', lc.Limit), jsonStringUTF8Bytes('中', lc.Limit+1))
			case "string-4byte":
				lim := canonical.Limits{MaxStringLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonStringUTF8Bytes('\U0001F600', lc.Limit), jsonStringUTF8Bytes('\U0001F600', lc.Limit+1))
			case "object-key":
				lim := canonical.Limits{MaxStringLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonObjectWithKeyBytes('k', lc.Limit), jsonObjectWithKeyBytes('k', lc.Limit+1))
			case "object-key-4byte":
				lim := canonical.Limits{MaxStringLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonObjectWithKeyBytes('\U0001F600', lc.Limit), jsonObjectWithKeyBytes('\U0001F600', lc.Limit+1))
			case "array":
				lim := canonical.Limits{MaxArrayLen: lc.Limit}
				assertParseAcceptReject(t, lim, jsonIntArray(lc.Limit), jsonIntArray(lc.Limit+1))
			case "object":
				lim := canonical.Limits{MaxObjectKeys: lc.Limit}
				assertParseAcceptReject(t, lim, jsonObjectOfSize(lc.Limit), jsonObjectOfSize(lc.Limit+1))
			case "depth":
				lim := canonical.Limits{MaxDepth: lc.Limit}
				assertParseAcceptReject(t, lim, jsonNestedArray(lc.Limit), jsonNestedArray(lc.Limit+1))
			case "bytes":
				lim := canonical.Limits{MaxBytes: int64(lc.Limit)}
				assertParseAcceptReject(t, lim, jsonBytesOfLength(lc.Limit), jsonBytesOfLength(lc.Limit+1))
			default:
				t.Fatalf("case %q: unknown limit kind %q", lc.Name, lc.Kind)
			}
		})
	}
}

func assertParseAcceptReject(t *testing.T, lim canonical.Limits, atLimit, overLimit []byte) {
	t.Helper()
	if _, err := canonical.ParseWithLimits(atLimit, lim); err != nil {
		t.Errorf("input exactly at the limit was rejected: %v", err)
	}
	if _, err := canonical.ParseWithLimits(overLimit, lim); err == nil {
		t.Error("input one over the limit was accepted")
	}
}

// jsonString returns a JSON string literal of exactly n ASCII characters, so byte
// length and rune count coincide
func jsonString(n int) []byte {
	return append(append([]byte{'"'}, bytes.Repeat([]byte{'a'}, n)...), '"')
}

// utf8BytesOfLength returns a string whose UTF-8 encoding is exactly totalBytes long,
// built from as many whole copies of r as fit and padded with ASCII 'x' to reach the
// exact count. For a multi-byte r the rune count is far below totalBytes. The
// string-2byte, string-3byte, string-4byte and object-key-*byte limitCase kinds use it
// to show that the length bound counts UTF-8 bytes, not runes.
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

// jsonStringUTF8Bytes returns a JSON string literal whose UTF-8 content, excluding the
// quote bytes, is exactly totalBytes long and built from rune r
func jsonStringUTF8Bytes(r rune, totalBytes int) []byte {
	return append(append([]byte{'"'}, []byte(utf8BytesOfLength(r, totalBytes))...), '"')
}

// jsonObjectWithKeyBytes returns a single-property JSON object whose key is exactly
// totalBytes of UTF-8, built from rune r
func jsonObjectWithKeyBytes(r rune, totalBytes int) []byte {
	key, _ := json.Marshal(utf8BytesOfLength(r, totalBytes))
	return []byte(`{` + string(key) + `:0}`)
}

// jsonIntArray returns a JSON array literal with exactly n integer elements
func jsonIntArray(n int) []byte {
	elems := make([]string, n)
	for i := range elems {
		elems[i] = "0"
	}
	return []byte("[" + strings.Join(elems, ",") + "]")
}

// jsonObjectOfSize returns a JSON object literal with exactly n distinct properties
func jsonObjectOfSize(n int) []byte {
	pairs := make([]string, n)
	for i := range pairs {
		pairs[i] = fmt.Sprintf("%q:0", fmt.Sprintf("k%d", i))
	}
	return []byte("{" + strings.Join(pairs, ",") + "}")
}

// jsonNestedArray returns a JSON value whose innermost array sits at nesting depth
// exactly n, with the root array at depth 0. The canonical parser increments depth once
// per container level below the root, so n levels below the outermost array take n+1
// bracket pairs (see canonical.ParseWithLimits).
func jsonNestedArray(n int) []byte {
	return []byte(strings.Repeat("[", n+1) + strings.Repeat("]", n+1))
}

// jsonBytesOfLength returns a valid JSON document, a single string literal including
// its two quote bytes, whose total length is exactly n bytes. It needs n >= 2.
func jsonBytesOfLength(n int) []byte {
	return jsonString(n - 2)
}
