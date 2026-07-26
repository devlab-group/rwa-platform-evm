package profile

import "testing"

// goldBarProfile is the worked gold-bar Asset Profile used as a fixture.
const goldBarProfile = `{
  "profileVersion": "1.0",
  "projectId": "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61",
  "assetType": "allocated-gold-bar",
  "tokenUnit": "gram",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "displayFields": [
    { "label": "Serial", "pointer": "/serialNumber" },
    { "label": "Weight", "pointer": "/weightGrams" },
    { "label": "Purity", "pointer": "/purity" }
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

func TestValidate_GoldBarProfile(t *testing.T) {
	p, err := Validate([]byte(goldBarProfile))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if p.ProjectID != "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61" {
		t.Errorf("ProjectID = %q", p.ProjectID)
	}
	if p.TokenDecimals != 18 {
		t.Errorf("TokenDecimals = %d, want 18", p.TokenDecimals)
	}
	if len(p.DisplayFields) != 3 {
		t.Fatalf("DisplayFields = %d, want 3", len(p.DisplayFields))
	}
	if p.DisplayFields[0].Label != "Serial" || p.DisplayFields[0].Pointer != "/serialNumber" {
		t.Errorf("DisplayFields[0] = %+v", p.DisplayFields[0])
	}
	if p.AssetSchema == nil {
		t.Fatal("AssetSchema is nil")
	}
	if err := p.AssetSchema.ValidateBytes([]byte(`{"serialNumber":"1","weightGrams":"10","purity":"999.9"}`)); err != nil {
		t.Errorf("compiled assetSchema rejected valid asset: %v", err)
	}
	if len(p.CanonicalBytes) == 0 {
		t.Error("CanonicalBytes is empty")
	}
	var zero [32]byte
	if p.Digest == zero {
		t.Error("Digest was not computed")
	}
}

func TestValidate_MissingRequiredField(t *testing.T) {
	bad := `{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"g","tokenDecimals":18,"assetSchema":{}}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for missing recordIdLabel")
	}
}

func TestValidate_WrongProfileVersion(t *testing.T) {
	bad := `{"profileVersion":"2.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"g","tokenDecimals":18,"recordIdLabel":"x","assetSchema":{}}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for wrong profileVersion")
	}
}

func TestValidate_BadProjectID(t *testing.T) {
	bad := `{"profileVersion":"1.0","projectId":"not-a-uuid","assetType":"x","tokenUnit":"g","tokenDecimals":18,"recordIdLabel":"x","assetSchema":{}}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for non-UUID projectId")
	}
}

func TestValidate_TokenDecimalsOutOfRange(t *testing.T) {
	bad := `{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"g","tokenDecimals":37,"recordIdLabel":"x","assetSchema":{}}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for tokenDecimals > 36")
	}
}

func TestValidate_AdditionalTopLevelPropertyRejected(t *testing.T) {
	bad := `{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"g","tokenDecimals":18,"recordIdLabel":"x","assetSchema":{},"extra":1}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for additional top-level property")
	}
}

func TestValidate_AssetSchemaRejectsForbiddenKeyword(t *testing.T) {
	bad := `{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"g","tokenDecimals":18,"recordIdLabel":"x","assetSchema":{"oneOf":[{"type":"string"}]}}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error: assetSchema uses forbidden keyword oneOf")
	}
}

// jsonUnicodeEscape builds a JSON \u-escape for a code point, e.g. "001b" for
// ESC. Written this way so no test file in this repo has to carry a raw
// control character in its source.
func jsonUnicodeEscape(code string) string {
	return "\\" + "u" + code
}

// TestValidate_RejectsControlCharInTokenUnit rejects a control character in a
// rendered string field at parse time. tokenUnit is the sharp case: the review
// screen prints it on the same line as the normalized amount, and it is not
// pinned by the --policy trust root, so a carriage return there could repaint
// the amount the auditor is confirming.
func TestValidate_RejectsControlCharInTokenUnit(t *testing.T) {
	for _, unit := range []string{
		`USD\r  amount (normalized)  : 1.500000 USD`, // carriage return
		"USD" + jsonUnicodeEscape("001b") + "[3A",    // ANSI cursor-up
		"USD" + jsonUnicodeEscape("007f"),            // DEL
		"USD" + jsonUnicodeEscape("0085"),            // C1 NEL
		`USD\t`,                                      // tab
	} {
		bad := `{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"` + unit + `","tokenDecimals":18,"recordIdLabel":"x","assetSchema":{}}`
		if _, err := Validate([]byte(bad)); err == nil {
			t.Errorf("expected error for tokenUnit %q", unit)
		}
	}
}

func TestValidate_RejectsControlCharInDisplayFieldLabel(t *testing.T) {
	label := "Serial" + jsonUnicodeEscape("001b") + "[31m"
	bad := `{"profileVersion":"1.0","projectId":"4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61","assetType":"x","tokenUnit":"g","tokenDecimals":18,"recordIdLabel":"x","displayFields":[{"label":"` + label + `","pointer":"/serialNumber"}],"assetSchema":{}}`
	if _, err := Validate([]byte(bad)); err == nil {
		t.Fatal("expected error for a display-field label containing an ANSI escape")
	}
}
