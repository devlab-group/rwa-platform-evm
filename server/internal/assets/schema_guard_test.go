package assets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// conformanceVectors mirrors shared/vectors/schema-dialect-conformance.json:
// compilation, instance validation, structural boundaries and nested envelopes.
// dialect_differential_test.go covers the non-compilation cases.
type conformanceVectors struct {
	Dialect string `json:"dialect"`
	Cases   []struct {
		Name   string          `json:"name"`
		Expect string          `json:"expect"` // "accept" | "reject"
		Schema json.RawMessage `json:"schema"`
	} `json:"cases"`

	// Limits mirrors shared/schemas/structural-limits.json; both binaries must
	// hardcode these values (checked in TestDialectLimitsMatchFrozenConstants)
	Limits struct {
		MaxUncompressedBytes int `json:"maxUncompressedBytes"`
		MaxDepth             int `json:"maxDepth"`
		MaxStringLength      int `json:"maxStringLength"`
		MaxArrayItems        int `json:"maxArrayItems"`
		MaxObjectProperties  int `json:"maxObjectProperties"`
	} `json:"limits"`

	// InstanceCases validate a JSON instance against a compiled assetSchema
	InstanceCases []struct {
		Name        string          `json:"name"`
		Schema      json.RawMessage `json:"schema"`
		Instance    json.RawMessage `json:"instance"`
		ExpectValid string          `json:"expectValid"` // "valid" | "invalid"
	} `json:"instanceCases"`

	// LimitCases describe a boundary that buildLimitBoundary constructs in code,
	// since a document of exactly 1 MiB or 4096 items is impractical to store
	LimitCases []struct {
		Name  string `json:"name"`
		Kind  string `json:"kind"` // "string" | "string-2byte" | "string-3byte" | "string-4byte" | "array" | "object" | "object-key" | "object-key-4byte" | "depth" | "bytes"
		Limit int    `json:"limit"`
		Note  string `json:"note"`
	} `json:"limitCases"`

	// EnvelopeCases test full profiles and metadata through ValidateProfile or
	// ValidateRecord, catching unknown fields in displayFields[], issuance and proofs[]
	// beyond schema compilation
	EnvelopeCases []struct {
		Name        string          `json:"name"`
		Target      string          `json:"target"` // "profile" | "metadata"
		Document    json.RawMessage `json:"document"`
		ExpectValid string          `json:"expectValid"` // "valid" | "invalid"
		Note        string          `json:"note"`
	} `json:"envelopeCases"`
}

func loadConformanceVectors(t *testing.T) conformanceVectors {
	t.Helper()
	p := filepath.Join("..", "..", "..", "shared", "vectors", "schema-dialect-conformance.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	var v conformanceVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing %s: %v", p, err)
	}
	if len(v.Cases) == 0 {
		t.Fatalf("%s has no cases", p)
	}
	return v
}

// TestDialectConformance compares each vector's expect with the guard and Draft 2020-12
// compiler. The signer tests the same vectors; either mismatch blocks release. Keep
// "Dialect" in test names: make dialect-check runs go test ./internal/assets/... -run
// Dialect.
func TestDialectConformance(t *testing.T) {
	vectors := loadConformanceVectors(t)
	if vectors.Dialect != "rwa-profile-assetSchema/1" {
		t.Fatalf("dialect = %q, want rwa-profile-assetSchema/1", vectors.Dialect)
	}

	for _, c := range vectors.Cases {
		t.Run(c.Name, func(t *testing.T) {
			guardErr := guardAssetSchema(c.Schema)
			var compileErr error
			if guardErr == nil {
				_, compileErr = compileAssetSchema(c.Schema)
			}
			accepted := guardErr == nil && compileErr == nil

			switch c.Expect {
			case "accept":
				if !accepted {
					err := guardErr
					if err == nil {
						err = compileErr
					}
					t.Fatalf("expected accept, got reject: %v", err)
				}
			case "reject":
				if accepted {
					t.Fatalf("expected reject, got accept")
				}
			default:
				t.Fatalf("unknown expect %q", c.Expect)
			}
		})
	}
}

// TestGuardRejectsEmptyAndDuplicateTypeArray checks the guard on its own, without
// the jsonschema compiler
func TestGuardRejectsEmptyAndDuplicateTypeArray(t *testing.T) {
	for _, raw := range []string{`{"type":[]}`, `{"type":["string","string"]}`} {
		if err := guardAssetSchema([]byte(raw)); err == nil {
			t.Errorf("guardAssetSchema(%s) accepted, want reject", raw)
		}
	}
	if err := guardAssetSchema([]byte(`{"type":["string","null"]}`)); err != nil {
		t.Errorf("valid type array rejected: %v", err)
	}
}

// diamondSchema has levels definitions that each refer twice to the next, so a full
// expansion visits 2^levels nodes
func diamondSchema(levels int) []byte {
	var defs []string
	for i := 0; i < levels-1; i++ {
		defs = append(defs, fmt.Sprintf(`"d%d":{"properties":{"a":{"$ref":"#/$defs/d%d"},"b":{"$ref":"#/$defs/d%d"}}}`, i, i+1, i+1))
	}
	defs = append(defs, fmt.Sprintf(`"d%d":{"type":"string"}`, levels-1))
	return []byte(`{"$defs":{` + strings.Join(defs, ",") + `},"properties":{"a":{"$ref":"#/$defs/d0"}}}`)
}

// TestDialectDiamondRefsStopAtNodeCap refuses a fan-out of shared definitions at the node
// cap, within the test's time budget, as the signer does
func TestDialectDiamondRefsStopAtNodeCap(t *testing.T) {
	start := time.Now()
	err := guardAssetSchema(diamondSchema(30))
	if err == nil || !strings.Contains(err.Error(), "maximum node count of 10000") {
		t.Fatalf("err = %v, want the node cap", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v to refuse the schema", d)
	}
}

// TestDialectCycleThroughDefsOfTarget refuses a definition nested in a $ref target that
// refers back to the target. The earlier guard restarted its cycle check inside $defs
// and recursed until memory ran out on this 70-byte schema.
func TestDialectCycleThroughDefsOfTarget(t *testing.T) {
	err := guardAssetSchema([]byte(`{"$defs":{"t":{"$defs":{"x":{"$ref":"#/$defs/t"}}}}}`))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err = %v, want a $ref cycle error", err)
	}
}

// TestPrepareAssetSchemaKeepsDataAndNames removes $schema and $id only where they are
// schema keywords: property and definition names and members of const or default stay
func TestPrepareAssetSchemaKeepsDataAndNames(t *testing.T) {
	raw := `{"$schema":"s","$id":"i","properties":{"$id":{"type":"string"},"k":{"$id":"u","$schema":"s","type":"integer"}},` +
		`"$defs":{"$schema":{"type":"string"}},"default":{"$id":"d"},"examples":[{"$schema":"e"}]}`
	got, err := prepareAssetSchema([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal(got, &tree); err != nil {
		t.Fatal(err)
	}
	props := tree["properties"].(map[string]any)
	k := props["k"].(map[string]any)
	switch {
	case tree["$schema"] != nil || tree["$id"] != nil:
		t.Errorf("root keywords kept: %s", got)
	case k["$id"] != nil || k["$schema"] != nil:
		t.Errorf("nested keywords kept: %s", got)
	case props["$id"] == nil || tree["$defs"].(map[string]any)["$schema"] == nil:
		t.Errorf("a property or definition named like a keyword was removed: %s", got)
	case tree["default"].(map[string]any)["$id"] == nil || tree["examples"].([]any)[0].(map[string]any)["$schema"] == nil:
		t.Errorf("data members were removed: %s", got)
	}
}

// TestUnmarshalStrictRejectsKeyCaseVariants refuses a key that differs from a field only
// in case at every level, where encoding/json would take the last of the two
func TestUnmarshalStrictRejectsKeyCaseVariants(t *testing.T) {
	type inner struct {
		Amount string `json:"amount"`
	}
	type outer struct {
		Items []inner `json:"items"`
		Ptr   *inner  `json:"ptr,omitempty"`
	}
	for _, raw := range []string{
		`{"items":[{"amount":"1","Amount":"2"}]}`,
		`{"ptr":{"AMOUNT":"1"}}`,
		`{"Items":[]}`,
	} {
		var v outer
		if err := unmarshalStrict([]byte(raw), &v); err == nil {
			t.Errorf("unmarshalStrict(%s) accepted", raw)
		}
	}
	var v outer
	if err := unmarshalStrict([]byte(`{"items":[{"amount":"1"}],"ptr":{"amount":"2"}}`), &v); err != nil {
		t.Errorf("exact keys rejected: %v", err)
	}
}
