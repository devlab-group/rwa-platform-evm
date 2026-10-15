package metadata

import "testing"

// goldBarMetadata is a representative gold-bar metadata envelope
const goldBarMetadata = `{
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
    {
      "type": "custodian-audit-report",
      "sha256": "6d7fe6a9f0d3e0c1f9b0e3c8a2b5d4f7a1c6e9b3d8f2a5c7e0b4d9f6a3c8e1b5",
      "uri": "ipfs://optional-public-document-cid"
    }
  ],
  "createdAt": "2026-07-17T12:00:00Z"
}`

func TestValidate_GoldBarMetadata(t *testing.T) {
	m, err := Validate([]byte(goldBarMetadata))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if m.RecordID != "GOLD-BAR-12345" {
		t.Errorf("RecordID = %q", m.RecordID)
	}
	if m.IssuanceAmount != "1000000000000000000000" {
		t.Errorf("IssuanceAmount = %q", m.IssuanceAmount)
	}
	if m.IssuanceUnit != "gram" {
		t.Errorf("IssuanceUnit = %q", m.IssuanceUnit)
	}
	if len(m.Proofs) != 1 || m.Proofs[0].SHA256 != "6d7fe6a9f0d3e0c1f9b0e3c8a2b5d4f7a1c6e9b3d8f2a5c7e0b4d9f6a3c8e1b5" {
		t.Errorf("Proofs = %+v", m.Proofs)
	}
	var zero [32]byte
	if m.Digest == zero {
		t.Error("Digest was not computed")
	}
}

func TestValidate_BadRecordID(t *testing.T) {
	bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"has space","asset":{},"issuance":{"amount":"1","unit":"g"},"createdAt":"2026-07-17T12:00:00Z"}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for recordId with a space")
	}
}

func TestValidate_NonDecimalAmount(t *testing.T) {
	bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"1.5","unit":"g"},"createdAt":"2026-07-17T12:00:00Z"}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for non-integer issuance.amount")
	}
}

func TestValidate_BadProofSHA256(t *testing.T) {
	bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"1","unit":"g"},"proofs":[{"type":"t","sha256":"not-hex"}],"createdAt":"2026-07-17T12:00:00Z"}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for malformed proof sha256")
	}
}

func TestValidate_BadCreatedAt(t *testing.T) {
	bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"1","unit":"g"},"createdAt":"not-a-date"}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for malformed createdAt")
	}
}

func TestValidate_AdditionalTopLevelPropertyRejected(t *testing.T) {
	bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"1","unit":"g"},"createdAt":"2026-07-17T12:00:00Z","extra":1}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for additional top-level property")
	}
}

// amountMetadata sets issuance.amount for boundary tests
func amountMetadata(amt string) string {
	return `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"` + amt + `","unit":"g"},"createdAt":"2026-07-17T12:00:00Z"}`
}

// maxUint256Str is 2^256-1, the maximum value of a Solidity uint256
const maxUint256Str = "115792089237316195423570985008687907853269984665640564039457584007913129639935"

// overUint256Str is 2^256, one past the maximum uint256 value
const overUint256Str = "115792089237316195423570985008687907853269984665640564039457584007913129639936"

// Reject regex-valid zero amounts, matching server/internal/assets/record.go and
// SupplyController.mint's ZeroAmount()
func TestValidate_RejectsZeroAmount(t *testing.T) {
	if _, err := Validate([]byte(amountMetadata("0"))); err == nil {
		t.Fatal("expected error for a zero issuance.amount")
	}
}

// Accept "007" as decimal 7, matching the server's decimalAmountPattern and
// big.Int.SetString(..., 10)
func TestValidate_AcceptsLeadingZeroAmount(t *testing.T) {
	m, err := Validate([]byte(amountMetadata("007")))
	if err != nil {
		t.Fatalf("expected a leading-zero positive amount to be accepted (server parity): %v", err)
	}
	if m.IssuanceAmount != "007" {
		t.Errorf("IssuanceAmount = %q, want the original literal %q preserved", m.IssuanceAmount, "007")
	}
}

// Accept issuance.amount = 2^256-1, the uint256 boundary
func TestValidate_AcceptsMaxUint256Amount(t *testing.T) {
	if _, err := Validate([]byte(amountMetadata(maxUint256Str))); err != nil {
		t.Fatalf("expected issuance.amount at exactly 2^256-1 to be accepted: %v", err)
	}
}

// Reject digit strings above uint256, matching server/internal/assets/record.go
func TestValidate_RejectsAmountAboveUint256Max(t *testing.T) {
	if _, err := Validate([]byte(amountMetadata(overUint256Str))); err == nil {
		t.Fatal("expected error for an issuance.amount exceeding 2^256-1")
	}
}

// jsonUnicodeEscape emits a JSON \u escape such as "001b" for ESC, avoiding raw
// controls in source
func jsonUnicodeEscape(code string) string {
	return "\\" + "u" + code
}

// Reject controls in issuance.unit and proofs[].type before they can repaint review
// amounts or digests
func TestValidate_RejectsControlCharInIssuanceUnit(t *testing.T) {
	for _, unit := range []string{
		`g\r  amount : 1`,
		"g" + jsonUnicodeEscape("001b") + "[2A",
		"g" + jsonUnicodeEscape("007f"),
		"g" + jsonUnicodeEscape("0085"),
		`g\n`,
	} {
		bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"1","unit":"` + unit + `"},"createdAt":"2026-07-17T12:00:00Z"}`
		if _, err := Validate([]byte(bad)); err == nil {
			t.Errorf("expected error for issuance.unit %q", unit)
		}
	}
}

func TestValidate_RejectsControlCharInProofType(t *testing.T) {
	sha := "0000000000000000000000000000000000000000000000000000000000000000"
	typ := "assay" + jsonUnicodeEscape("001b") + "[3A"
	bad := `{"platformVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","recordId":"X-1","asset":{},"issuance":{"amount":"1","unit":"g"},"proofs":[{"type":"` + typ + `","sha256":"` + sha + `"}],"createdAt":"2026-07-17T12:00:00Z"}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for a proof type containing an ANSI escape")
	}
}
