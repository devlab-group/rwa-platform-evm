package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// envelopeCase models whole profile/metadata envelopeCases from
// shared/vectors/schema-dialect-conformance.json, beyond assetSchema-only checks
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

// Match shared profile envelopeCases, including unknown displayFields members. Keep
// "Dialect" in the name for make dialect-check and this test in profile to avoid a
// jsonschema import cycle.
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
