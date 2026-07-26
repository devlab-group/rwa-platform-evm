package assets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// conformanceVectors mirrors shared/vectors/schema-dialect-conformance.json
// (frozen). The corpus is differential rather than compile-only: schema
// compilation, instance validation, exact structural-limit boundaries, and
// nested-envelope decoding all have dedicated vector sets — see
// dialect_differential_test.go for the tests that consume those fields.
type conformanceVectors struct {
	Dialect string `json:"dialect"`
	Cases   []struct {
		Name   string          `json:"name"`
		Expect string          `json:"expect"` // "accept" | "reject"
		Schema json.RawMessage `json:"schema"`
	} `json:"cases"`

	// Limits mirrors shared/schemas/structural-limits.json; both binaries
	// MUST hardcode these exact values (asserted in TestDialectLimitsMatchFrozenConstants).
	Limits struct {
		MaxUncompressedBytes int `json:"maxUncompressedBytes"`
		MaxDepth             int `json:"maxDepth"`
		MaxStringLength      int `json:"maxStringLength"`
		MaxArrayItems        int `json:"maxArrayItems"`
		MaxObjectProperties  int `json:"maxObjectProperties"`
	} `json:"limits"`

	// InstanceCases validate a concrete JSON instance against a compiled
	// assetSchema (not merely whether the schema compiles).
	InstanceCases []struct {
		Name        string          `json:"name"`
		Schema      json.RawMessage `json:"schema"`
		Instance    json.RawMessage `json:"instance"`
		ExpectValid string          `json:"expectValid"` // "valid" | "invalid"
	} `json:"instanceCases"`

	// LimitCases describe a structural boundary to construct programmatically
	// (see dialect_differential_test.go's buildLimitBoundary) rather than a
	// literal document, since an exact 1 MiB/4096-item document isn't
	// practical to check into the repo.
	LimitCases []struct {
		Name  string `json:"name"`
		Kind  string `json:"kind"` // "string" | "string-2byte" | "string-3byte" | "string-4byte" | "array" | "object" | "object-key" | "object-key-4byte" | "depth" | "bytes"
		Limit int    `json:"limit"`
		Note  string `json:"note"`
	} `json:"limitCases"`

	// EnvelopeCases feed a full profile/metadata envelope document through
	// the real ValidateProfile/ValidateRecord entry points, catching
	// unknown-field/strict-decoding gaps that a bare schema-compile check
	// can't see (e.g. an extra field in displayFields[]/issuance/proofs[]).
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

// TestDialectConformance is a release gate: every vector's "expect" must match
// what guardAssetSchema + the server's Draft-2020-12 compiler actually do with
// the vector's schema. The signer runs the identical vectors against its own
// compiler (signer package jsonschema) under the same test name; a mismatch on
// either side is release-blocking. Name and package are load-bearing:
// the root `make dialect-check`/`make vectors-check` gate runs
// `go test ./internal/assets/... -run TestDialectConformance -count=1` —
// do not rename without updating that gate too.
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
