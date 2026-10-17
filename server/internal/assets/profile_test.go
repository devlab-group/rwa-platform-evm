package assets

import (
	"strings"
	"testing"
)

const goldGramProfile = `{
  "profileVersion": "1.0",
  "projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
  "assetType": "allocated-gold-bar",
  "tokenUnit": "gram",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "displayFields": [
    { "label": "Serial", "pointer": "/serialNumber" }
  ],
  "assetSchema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "additionalProperties": false,
    "required": ["serialNumber", "weightGrams", "purity"],
    "properties": {
      "serialNumber": { "type": "string", "minLength": 1 },
      "weightGrams": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" },
      "purity": { "type": "string", "pattern": "^[0-9]+(\\.[0-9]+)?$" }
    }
  }
}`

func TestValidateProfileAcceptsSpecExample(t *testing.T) {
	result, profile := ValidateProfile([]byte(goldGramProfile))
	if !result.Valid {
		t.Fatalf("expected valid, got errors: %v", result.Errors)
	}
	if profile == nil {
		t.Fatal("expected non-nil profile")
	}
	if result.ProfileDigest == "" || !strings.HasPrefix(result.ProfileDigest, "0x") {
		t.Errorf("ProfileDigest = %q", result.ProfileDigest)
	}
	if result.CID == "" {
		t.Errorf("CID is empty")
	}
	if profile.TokenDecimals != 18 {
		t.Errorf("TokenDecimals = %d, want 18", profile.TokenDecimals)
	}
}

func TestValidateProfileRejectsMissingRequired(t *testing.T) {
	result, profile := ValidateProfile([]byte(`{"profileVersion":"1.0"}`))
	if result.Valid {
		t.Fatal("expected invalid")
	}
	if profile != nil {
		t.Fatal("expected nil profile")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected errors")
	}
}

func TestValidateProfileRejectsDisallowedComposition(t *testing.T) {
	profile := `{
      "profileVersion": "1.0",
      "projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
      "assetType": "x",
      "tokenUnit": "u",
      "tokenDecimals": 0,
      "recordIdLabel": "id",
      "assetSchema": {
        "type": "object",
        "oneOf": [ {"required": ["a"]}, {"required": ["b"]} ]
      }
    }`
	result, _ := ValidateProfile([]byte(profile))
	if result.Valid {
		t.Fatal("expected invalid for oneOf in assetSchema")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "oneOf") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning oneOf, got %v", result.Errors)
	}
}
