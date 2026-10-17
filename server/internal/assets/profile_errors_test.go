package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	gocid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// Derive SHA-256 and raw/sha2-256 CIDv1 independently, as docs/spec/asset-profile.md specifies.
// This fixture's ASCII keys and plain values make encoding/json's compact sorted output JCS.
func TestValidateProfileDigestAndCIDMatchAnIndependentDerivation(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramProfile), &m); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	mh, err := multihash.Sum(canonical, multihash.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}

	result, profile := ValidateProfile([]byte(goldGramProfile))
	if !result.Valid {
		t.Fatal(result.Errors)
	}
	if want := "0x" + hex.EncodeToString(sum[:]); result.ProfileDigest != want || profile.DigestHex() != want {
		t.Errorf("digest = %s / %s, want %s", result.ProfileDigest, profile.DigestHex(), want)
	}
	if want := gocid.NewCidV1(gocid.Raw, mh).String(); result.CID != want {
		t.Errorf("CID = %s, want %s", result.CID, want)
	}
}

// Each case breaks one rule of an otherwise valid profile and must be rejected for it
func TestValidateProfileRejectsEachViolationForItsOwnReason(t *testing.T) {
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal([]byte(goldGramProfile), &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"wrong profileVersion", mutate(func(m map[string]any) { m["profileVersion"] = "2.0" }), "profileVersion must be"},
		{"unknown top-level field", mutate(func(m map[string]any) { m["unexpectedField"] = "nope" }), "unknown top-level field"},
		{"token decimals above 36", mutate(func(m map[string]any) { m["tokenDecimals"] = 37 }), "tokenDecimals must be an integer in [0,36]"},
		{"remote $ref", []byte(`{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"u","tokenDecimals":0,"recordIdLabel":"id",
			"assetSchema":{"type":"object","properties":{"a":{"$ref":"https://evil.example/schema.json"}}}}`), "$ref at $.properties.a must be a local JSON pointer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, profile := ValidateProfile(tt.raw)
			if result.Valid || profile != nil {
				t.Fatalf("profile accepted: %+v", result)
			}
			for _, e := range result.Errors {
				if strings.Contains(e, tt.want) {
					return
				}
			}
			t.Errorf("errors = %v, want one mentioning %q", result.Errors, tt.want)
		})
	}
}
