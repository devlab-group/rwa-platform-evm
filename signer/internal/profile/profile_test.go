package profile

import "testing"

// goldBarProfile is the worked gold-bar Asset Profile used as a fixture
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

// jsonUnicodeEscape emits a JSON \u escape, e.g. "001b" for ESC, without raw controls
// in source
func jsonUnicodeEscape(code string) string {
	return "\\" + "u" + code
}

// Reject tokenUnit controls before review; profileDigest binding cannot prevent cursor
// manipulation beside normalized amounts
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

func TestValidLEI(t *testing.T) {
	for lei, want := range map[string]bool{
		"5493000EXAMPLE0LEI67": true,
		"506700GE1G29325QX363": true,
		"5493000EXAMPLE0LEI68": false,
		"5493000example0lei67": false,
		"5493000EXAMPLE0LEIAB": false,
		"5493000EXAMPLE0LEI6":  false,
	} {
		if got := validLEI(lei); got != want {
			t.Errorf("validLEI(%q) = %v, want %v", lei, got, want)
		}
	}
}

func withLegal(legal string) []byte {
	s := goldBarProfile
	i := len(`{`)
	return []byte(s[:i] + `"legal":` + legal + `,` + s[i:])
}

const sum = `"sha256":"a4244f1bab68afd39467ad8bfa7904024342d9758de5e7b0af41819a02347cbe"`

func TestValidate_Legal(t *testing.T) {
	ok := []string{
		`{"wrapper":"claim"}`,
		`{"wrapper":"share","issuerLei":"506700GE1G29325QX363","holderRights":"x","maturityDate":"2028-02-29",` +
			`"redemptionTerms":{"uri":"https://a/b",` + sum + `},"rating":{"provider":"P","grade":"AA","uri":"https://a/r",` + sum + `}}`,
	}
	for _, l := range ok {
		p, err := Validate(withLegal(l))
		if err != nil || p.Legal == nil {
			t.Errorf("%s: err=%v legal=%v", l, err, p != nil && p.Legal != nil)
		}
	}
	bad := map[string]string{
		"unknown member":    `{"wrapper":"share","x":1}`,
		"missing wrapper":   `{"holderRights":"x"}`,
		"unknown wrapper":   `{"wrapper":"bond"}`,
		"bad LEI checksum":  `{"wrapper":"share","issuerLei":"5493000EXAMPLE0LEI68"}`,
		"empty rights":      `{"wrapper":"share","holderRights":""}`,
		"newline in rights": `{"wrapper":"share","holderRights":"a\nb"}`,
		"bidi in rights":    `{"wrapper":"share","holderRights":"a‮b"}`,
		"long rights":       `{"wrapper":"share","holderRights":"` + repeat("a", 2049) + `"}`,
		"feb 30":            `{"wrapper":"share","maturityDate":"2030-02-30"}`,
		"short date":        `{"wrapper":"share","maturityDate":"2030-2-3"}`,
		"uri space":         `{"wrapper":"share","redemptionTerms":{"uri":"https://a/b c",` + sum + `}}`,
		"ref no sha":        `{"wrapper":"share","redemptionTerms":{"uri":"https://a/b"}}`,
		"upper sha":         `{"wrapper":"share","redemptionTerms":{"uri":"https://a/b","sha256":"A4244F1BAB68AFD39467AD8BFA7904024342D9758DE5E7B0AF41819A02347CBE"}}`,
		"ref extra":         `{"wrapper":"share","redemptionTerms":{"uri":"https://a/b",` + sum + `,"x":1}}`,
		"rating no uri":     `{"wrapper":"share","rating":{"provider":"P",` + sum + `}}`,
		"rating no prov":    `{"wrapper":"share","rating":{"uri":"https://a",` + sum + `}}`,
		"empty grade":       `{"wrapper":"share","rating":{"provider":"P","grade":"","uri":"https://a",` + sum + `}}`,
		"legal not object":  `[]`,
	}
	for name, l := range bad {
		if _, err := Validate(withLegal(l)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestValidate_LegalAbsentUnchanged(t *testing.T) {
	p, err := Validate([]byte(goldBarProfile))
	if err != nil || p.Legal != nil {
		t.Fatalf("err=%v legal=%v", err, p.Legal)
	}
}
