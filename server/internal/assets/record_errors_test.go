package assets

import (
	"encoding/json"
	"strings"
	"testing"
)

// Break one rule per case and require its error, excluding failures for unrelated reasons
func TestValidateRecordRejectsEachViolationForItsOwnReason(t *testing.T) {
	profile := mustProfile(t)
	const wantAmount = "1000000000000000000000"
	tests := []struct {
		name       string
		mutate     func(m map[string]any)
		amount     string
		projectID  string
		wantReason string
	}{
		{"project id differs from the profile", func(m map[string]any) {}, wantAmount, "00000000-0000-0000-0000-000000000000",
			"projectId does not match the immutable project profile"},
		{"bad recordId pattern", func(m map[string]any) { m["recordId"] = "bad id with spaces!" }, wantAmount, profile.ProjectID,
			"recordId must match"},
		{"asset misses a required field", func(m map[string]any) { delete(m["asset"].(map[string]any), "purity") }, wantAmount, profile.ProjectID,
			"missing properties: 'purity'"},
		{"proof hash is not hex", func(m map[string]any) { m["proofs"] = []map[string]any{{"type": "x", "sha256": "not-hex"}} }, wantAmount, profile.ProjectID,
			"proofs[0].sha256 must match"},
		{"fractional amount", func(m map[string]any) { m["issuance"].(map[string]any)["amount"] = "1.5" }, "", profile.ProjectID,
			"issuance.amount must match"},
		{"zero amount", func(m map[string]any) { m["issuance"].(map[string]any)["amount"] = "0" }, "", profile.ProjectID,
			"positive integer"},
		{"amount above uint256", func(m map[string]any) {
			m["issuance"].(map[string]any)["amount"] = "115792089237316195423570985008687907853269984665640564039457584007913129639936"
		}, "", profile.ProjectID, "no greater than 2^256-1"},
		{"unknown issuance field", func(m map[string]any) { m["issuance"].(map[string]any)["surprise"] = true }, "", profile.ProjectID,
			"issuance must be an object with string amount/unit and no additional fields"},
		{"unknown proofs field", func(m map[string]any) {
			m["proofs"] = []map[string]any{{"type": "x", "sha256": strings.Repeat("a", 64), "surprise": true}}
		}, "", profile.ProjectID, "proofs must be an array of {type, sha256, uri?} with no additional fields"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(goldGramRecord), &m); err != nil {
				t.Fatal(err)
			}
			tt.mutate(m)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			result, _ := ValidateRecord(profile, raw, tt.amount, tt.projectID)
			if result.Valid {
				t.Fatal("record was accepted")
			}
			for _, e := range result.Errors {
				if strings.Contains(e, tt.wantReason) {
					return
				}
			}
			t.Errorf("errors = %v, want one containing %q", result.Errors, tt.wantReason)
		})
	}
}

func TestValidateRecordRejectsAmountMismatchWithTheRequest(t *testing.T) {
	profile := mustProfile(t)
	result, _ := ValidateRecord(profile, []byte(goldGramRecord), "999", profile.ProjectID)
	if result.Valid {
		t.Fatal("expected invalid for amount mismatch")
	}
	for _, e := range result.Errors {
		if strings.Contains(e, "issuance.amount") {
			return
		}
	}
	t.Errorf("errors = %v, want an issuance.amount mismatch", result.Errors)
}
