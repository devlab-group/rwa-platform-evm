package assets

import (
	"strings"
	"testing"
)

func TestLEIChecksum(t *testing.T) {
	for lei, want := range map[string]bool{
		"5493000EXAMPLE0LEI67": true,
		"506700GE1G29325QX363": true,
		"5493000EXAMPLE0LEI68": false,
	} {
		if got := leiChecksumOK(lei); got != want {
			t.Errorf("leiChecksumOK(%s) = %v, want %v", lei, got, want)
		}
	}
}

func profileWithLegal(legal string) []byte {
	return []byte(strings.Replace(goldGramProfile, `"displayFields"`, `"legal": `+legal+`, "displayFields"`, 1))
}

func TestValidateProfileLegal(t *testing.T) {
	sha := strings.Repeat("a", 64)
	ref := `{"uri":"https://x.example/t.pdf","sha256":"` + sha + `"}`
	cases := map[string]struct {
		legal string
		valid bool
	}{
		"wrapper only":       {`{"wrapper":"claim"}`, true},
		"full":               {`{"wrapper":"share","issuerLei":"506700GE1G29325QX363","holderRights":"pro rata","redemptionTerms":` + ref + `,"maturityDate":"2030-12-31","rating":{"provider":"P","grade":"A","uri":"https://r.example","sha256":"` + sha + `"}}`, true},
		"null":               {`null`, false},
		"missing wrapper":    {`{}`, false},
		"bad wrapper":        {`{"wrapper":"bond"}`, false},
		"unknown member":     {`{"wrapper":"share","x":1}`, false},
		"lei checksum":       {`{"wrapper":"share","issuerLei":"5493000EXAMPLE0LEI68"}`, false},
		"lei lowercase":      {`{"wrapper":"share","issuerLei":"5493000example0lei67"}`, false},
		"empty rights":       {`{"wrapper":"share","holderRights":""}`, false},
		"newline in rights":  {`{"wrapper":"share","holderRights":"a\nb"}`, false},
		"long rights":        {`{"wrapper":"share","holderRights":"` + strings.Repeat("a", 2049) + `"}`, false},
		"impossible date":    {`{"wrapper":"share","maturityDate":"2030-02-30"}`, false},
		"date with time":     {`{"wrapper":"share","maturityDate":"2030-01-01T00:00:00Z"}`, false},
		"ref extra member":   {`{"wrapper":"share","redemptionTerms":{"uri":"https://x","sha256":"` + sha + `","a":1}}`, false},
		"ref space in uri":   {`{"wrapper":"share","redemptionTerms":{"uri":"https://x y","sha256":"` + sha + `"}}`, false},
		"ref uppercase hash": {`{"wrapper":"share","redemptionTerms":{"uri":"https://x","sha256":"` + strings.Repeat("A", 64) + `"}}`, false},
		"rating no provider": {`{"wrapper":"share","rating":{"uri":"https://x","sha256":"` + sha + `"}}`, false},
		"rating no hash":     {`{"wrapper":"share","rating":{"provider":"P","uri":"https://x"}}`, false},
		"rating empty grade": {`{"wrapper":"share","rating":{"provider":"P","grade":"","uri":"https://x","sha256":"` + sha + `"}}`, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res, p := ValidateProfile(profileWithLegal(c.legal))
			if res.Valid != c.valid {
				t.Fatalf("valid = %v, want %v (errors %v)", res.Valid, c.valid, res.Errors)
			}
			if !c.valid {
				// the profile is otherwise valid, so every error must come from the legal object
				for _, e := range res.Errors {
					if !strings.Contains(e, "legal") {
						t.Errorf("error %q is not about the legal object", e)
					}
				}
			}
			if c.valid && (p.Legal == nil || p.Legal.Wrapper == "") {
				t.Fatal("legal not kept on Profile")
			}
		})
	}
}
