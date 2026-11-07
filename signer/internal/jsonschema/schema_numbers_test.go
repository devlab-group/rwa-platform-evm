package jsonschema

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/rwa-platform/signer/internal/canonical"
)

// valueKey must give two values one key exactly when the dialect treats them as equal
func TestValueKeySeparatesAndJoinsValues(t *testing.T) {
	key := func(raw string, rat func(json.Number) (*big.Rat, error)) string {
		t.Helper()
		v, err := canonical.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		k, err := valueKey(v, rat)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	same := [][2]string{
		{`1`, `1.0`},
		{`0.5`, `5e-1`},
		{`{"a":1,"b":[2]}`, `{"b":[2.0],"a":1}`},
		{`0`, `-0`},
	}
	for _, p := range same {
		if a, b := key(p[0], schemaRat), key(p[1], schemaRat); a != b {
			t.Errorf("%s and %s have different keys: %q %q", p[0], p[1], a, b)
		}
	}
	different := [][2]string{
		{`["a","b"]`, `["ab"]`},
		{`["ab"]`, `["a","b"]`},
		{`[1,2]`, `[12]`},
		{`{"a":"b"}`, `{"a":"b","c":null}`},
		{`"1"`, `1`},
		{`null`, `false`},
		{`true`, `false`},
		{`[]`, `{}`},
		{`0.1`, `0.10000000000000000001`},
	}
	for _, p := range different {
		if a, b := key(p[0], schemaRat), key(p[1], schemaRat); a == b {
			t.Errorf("%s and %s share key %q", p[0], p[1], a)
		}
	}
	// instance numbers are read as the nearest float64
	if a, b := key(`0.1`, instanceRat), key(`0.10000000000000000001`, instanceRat); a != b {
		t.Errorf("instance 0.1 and 0.10000000000000000001 differ: %q %q", a, b)
	}
}

func TestNumberLiteralsOutsideTheSupportedRange(t *testing.T) {
	for _, lit := range []json.Number{"1e400", "-1e400"} {
		if _, err := schemaRat(lit); err == nil {
			t.Errorf("schemaRat(%s) accepted", lit)
		}
		if _, err := instanceRat(lit); err == nil {
			t.Errorf("instanceRat(%s) accepted", lit)
		}
	}
	if _, err := schemaRat("1e-999999999"); err == nil {
		t.Error("schemaRat accepted an exponent too small for an exact rational")
	}
	if r, err := instanceRat("1e-999999999"); err != nil || r.Sign() != 0 {
		t.Errorf("instanceRat(1e-999999999) = %v, %v; want 0", r, err)
	}
}
