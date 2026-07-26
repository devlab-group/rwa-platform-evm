package assets

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestValidateRecordRejectsAmountMismatch(t *testing.T) {
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, []byte(goldGramRecord), "999", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for amount mismatch")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "issuance.amount") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected issuance.amount error, got %v", result.Errors)
	}
}

func TestValidateRecordRejectsProjectIDMismatch(t *testing.T) {
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, []byte(goldGramRecord), "1000000000000000000000", "00000000-0000-0000-0000-000000000000")
	if result.Valid {
		t.Fatal("expected invalid for projectId mismatch")
	}
}

func TestValidateRecordRejectsBadRecordIDPattern(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	m["recordId"] = "bad id with spaces!"
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "1000000000000000000000", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for bad recordId pattern")
	}
}

func TestValidateRecordRejectsAssetSchemaViolation(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	asset := m["asset"].(map[string]any)
	delete(asset, "purity") // required by assetSchema
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "1000000000000000000000", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for missing required asset field")
	}
}

func TestValidateRecordRejectsBadProofHash(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	m["proofs"] = []map[string]any{{"type": "x", "sha256": "not-hex"}}
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "1000000000000000000000", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for bad proof sha256")
	}
}

func TestValidateRecordRejectsFloatAmount(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	issuance := m["issuance"].(map[string]any)
	issuance["amount"] = "1.5"
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for non-integer amount string")
	}
}

// TestValidateRecordRejectsZeroAmount guards against persisting a record the
// contract will always reject: SupplyController.mint reverts ZeroAmount() on a
// zero attestation amount. "0" satisfies decimalAmountPattern (^[0-9]+$) but
// must still be rejected by the added semantic bound check.
func TestValidateRecordRejectsZeroAmount(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	issuance := m["issuance"].(map[string]any)
	issuance["amount"] = "0"
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for a zero issuance.amount")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "positive integer") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a positive-integer error, got %v", result.Errors)
	}
}

// TestValidateRecordRejectsAmountAboveUint256Max pins the other half of the
// bound: a syntactically ^[0-9]+$-matching numeral with more digits than fit
// in a uint256 must also be rejected, not silently truncated or left to fail
// later at ABI encoding.
func TestValidateRecordRejectsAmountAboveUint256Max(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	issuance := m["issuance"].(map[string]any)
	// 2^256 (one past the max uint256 value).
	issuance["amount"] = "115792089237316195423570985008687907853269984665640564039457584007913129639936"
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for an issuance.amount exceeding 2^256-1")
	}
}

// TestValidateRecordRejectsUnknownIssuanceField and
// TestValidateRecordRejectsUnknownProofsField pin the strict nested-envelope
// decoding (unmarshalStrict, strict.go) at the metadata.go call
// sites; dialect_differential_test.go's envelopeCases cover the equivalent
// displayFields[] case on the Profile side.
func TestValidateRecordRejectsUnknownIssuanceField(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	issuance := m["issuance"].(map[string]any)
	issuance["surprise"] = true
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for an unknown field inside issuance")
	}
}

func TestValidateRecordRejectsUnknownProofsField(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
		t.Fatal(err)
	}
	m["proofs"] = []map[string]any{{"type": "x", "sha256": strings.Repeat("a", 64), "surprise": true}}
	raw, _ := json.Marshal(m)
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, raw, "", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for an unknown field inside a proofs[] entry")
	}
}
