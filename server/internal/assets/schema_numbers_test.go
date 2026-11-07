package assets

import (
	"fmt"
	"testing"
)

// TestGuardLengthKeywordForms accepts plain non-negative integer literals for the length
// and count keywords and refuses every other spelling, as the signer does
func TestGuardLengthKeywordForms(t *testing.T) {
	for _, kw := range lengthKeywords {
		for lit, ok := range map[string]bool{
			`0`: true, `7`: true, `-0`: true,
			`1.0`: false, `1e1`: false, `1E1`: false, `-1`: false, `1.5`: false,
			`"1"`: false, `true`: false, `null`: false, `1e30`: false,
		} {
			err := guardAssetSchema([]byte(fmt.Sprintf(`{%q:%s}`, kw, lit)))
			if (err == nil) != ok {
				t.Errorf("%s: %s -> %v, accepted = %v", kw, lit, err, ok)
			}
		}
	}
}

// TestGuardNumberKeywordForms refuses a bound that is not a number and a number the
// compiler cannot hold as an exact rational, which it would otherwise drop silently
func TestGuardNumberKeywordForms(t *testing.T) {
	for _, kw := range numberKeywords {
		for lit, ok := range map[string]bool{
			`0`: true, `-1.5`: true, `1e300`: true, `0.10000000000000000001`: true,
			`"1"`: false, `true`: false, `1e-999999999`: false, `1e400`: false,
		} {
			err := guardAssetSchema([]byte(fmt.Sprintf(`{%q:%s}`, kw, lit)))
			if (err == nil) != ok {
				t.Errorf("%s: %s -> %v, accepted = %v", kw, lit, err, ok)
			}
		}
	}
}
