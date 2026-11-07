package auditpkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// An unpaired surrogate escape must fail in Canonicalize, as it does in the signer. The
// JCS library alone refuses a lone one but turns a mismatched pair such as
// "\ud800\ud800" into U+FFFD, which would give two documents one digest.
func TestJCSCIDSurrogateCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "vectors", "jcs-cid.json"))
	if err != nil {
		t.Fatalf("reading shared/vectors/jcs-cid.json: %v", err)
	}
	var v struct {
		SurrogateCases struct {
			Cases []struct {
				Name               string `json:"name"`
				Input              string `json:"input"`
				Expect             string `json:"expect"` // "accept" | "reject"
				CanonicalBytesUTF8 string `json:"canonicalBytesUtf8"`
			} `json:"cases"`
		} `json:"surrogateCases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing shared/vectors/jcs-cid.json: %v", err)
	}
	if len(v.SurrogateCases.Cases) == 0 {
		t.Fatal("jcs-cid.json has no surrogateCases")
	}
	for _, c := range v.SurrogateCases.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := Canonicalize([]byte(c.Input))
			switch c.Expect {
			case "reject":
				if err == nil {
					t.Fatalf("Canonicalize(%s) = %q, want an error", c.Input, got)
				}
			case "accept":
				if err != nil {
					t.Fatalf("Canonicalize(%s): %v", c.Input, err)
				}
				if string(got) != c.CanonicalBytesUTF8 {
					t.Fatalf("Canonicalize(%s) = %q, want %q", c.Input, got, c.CanonicalBytesUTF8)
				}
			default:
				t.Fatalf("unknown expect %q", c.Expect)
			}
		})
	}
}
