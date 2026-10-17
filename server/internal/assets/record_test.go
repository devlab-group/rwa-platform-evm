package assets

import "testing"

const goldGramRecord = `{
  "platformVersion": "1.0",
  "projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
  "recordId": "GOLD-BAR-12345",
  "asset": {
    "serialNumber": "12345",
    "weightGrams": "1000",
    "purity": "999.9"
  },
  "issuance": {
    "amount": "1000000000000000000000",
    "unit": "gram"
  },
  "proofs": [
    { "type": "custodian-audit-report", "sha256": "6d7fee5c9d9b436d940b346bd4b8c9b7b3f3c1ab8e1a4a2b1c0d9e8f7a6b5c4d" }
  ],
  "createdAt": "2026-07-17T12:00:00Z"
}`

func mustProfile(t *testing.T) *Profile {
	t.Helper()
	result, profile := ValidateProfile([]byte(goldGramProfile))
	if !result.Valid {
		t.Fatalf("profile fixture invalid: %v", result.Errors)
	}
	return profile
}

func TestValidateRecordAcceptsSpecExample(t *testing.T) {
	profile := mustProfile(t)
	result, meta := ValidateRecord(profile, []byte(goldGramRecord), "1000000000000000000000", profile.ProjectID)
	if !result.Valid {
		t.Fatalf("expected valid, got errors: %v", result.Errors)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata")
	}
	if meta.RecordID != "GOLD-BAR-12345" {
		t.Errorf("RecordID = %q", meta.RecordID)
	}
	if meta.DigestHex() == "" {
		t.Errorf("empty digest")
	}
}
