package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// envelopeCase mirrors one entry of shared/vectors/schema-dialect-conformance.json's
// "envelopeCases" array: strict-decoding parity for a whole profile or metadata
// document, not just its assetSchema
type envelopeCase struct {
	Name        string          `json:"name"`
	Target      string          `json:"target"` // "profile" | "metadata"
	ExpectValid string          `json:"expectValid"`
	Note        string          `json:"note"`
	Document    json.RawMessage `json:"document"`
}

type envelopeConformanceFile struct {
	Dialect       string         `json:"dialect"`
	EnvelopeCases []envelopeCase `json:"envelopeCases"`
}

// TestDialectEnvelopeConformance checks profile envelopeCases from the shared
// schema-dialect vectors, including unknown fields in displayFields objects.
// The server and signer must accept the same documents.
//
// Keep this test in profile: jsonschema cannot import profile without a cycle.
// make dialect-check runs both packages.
func TestDialectEnvelopeConformance(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "shared", "vectors", "schema-dialect-conformance.json"))
	if err != nil {
		t.Fatalf("resolving shared/vectors/schema-dialect-conformance.json path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading shared/vectors/schema-dialect-conformance.json: %v", err)
	}
	var file envelopeConformanceFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing conformance vectors: %v", err)
	}
	if file.Dialect != "rwa-profile-assetSchema/1" {
		t.Fatalf("unexpected dialect id %q", file.Dialect)
	}
	if len(file.EnvelopeCases) == 0 {
		t.Fatal("conformance vector file has no envelopeCases")
	}

	ran := 0
	for _, c := range file.EnvelopeCases {
		c := c
		if c.Target != "profile" {
			continue // metadata-target cases, if any, belong in internal/metadata
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			_, err := Validate(c.Document)
			switch c.ExpectValid {
			case "valid":
				if err != nil {
					t.Errorf("case %q: expected valid, got: %v", c.Name, err)
				}
			case "invalid":
				if err == nil {
					t.Errorf("case %q: expected invalid, but Validate accepted it", c.Name)
				}
			default:
				t.Fatalf("case %q: unknown expectValid value %q", c.Name, c.ExpectValid)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no envelopeCases targeted \"profile\"")
	}
}
