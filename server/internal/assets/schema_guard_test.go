package assets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// conformanceVectors mirrors shared/vectors/schema-dialect-conformance.json. It
// has separate vector sets for schema compilation, instance validation,
// structural-limit boundaries and nested-envelope decoding. The tests in
// dialect_differential_test.go use the fields besides the compile cases.
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

	// EnvelopeCases run a full profile or metadata document through
	// ValidateProfile or ValidateRecord, which catches unknown-field gaps that a
	// schema compile check cannot, such as an extra field in displayFields[],
	// issuance or proofs[]
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

// TestDialectConformance checks that every vector's "expect" matches what
// guardAssetSchema and the Draft 2020-12 compiler do with the vector's schema.
// The signer runs the same vectors against its own compiler under the same test
// name, and a mismatch on either side blocks a release. `make dialect-check`
// runs `go test ./internal/assets/... -run Dialect`, so keep "Dialect" in the
// names of the tests it should run.
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
