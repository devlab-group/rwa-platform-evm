package main

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/rwa-platform/signer/internal/canonical"
	"github.com/rwa-platform/signer/internal/profile"
	"github.com/rwa-platform/signer/internal/ui"
)

// Show normalized amounts: identical raw units represent different quantities at
// different tokenDecimals
func TestNormalizedAmount_MisleadingDecimals(t *testing.T) {
	amount := big.NewInt(1000000000000000000) // 1e18 raw units

	cases := []struct {
		decimals int
		want     string
	}{
		{decimals: 18, want: "1.000000000000000000"}, // 1 whole token
		{decimals: 0, want: "1000000000000000000"},   // 1 quintillion whole units
		{decimals: 2, want: "10000000000000000.00"},  // 1e16 whole units
		{decimals: 6, want: "1000000000000.000000"},  // 1e12 whole units
	}
	for _, c := range cases {
		got := normalizedAmount(amount, c.decimals)
		if got != c.want {
			t.Errorf("normalizedAmount(%s, %d) = %q, want %q", amount.String(), c.decimals, got, c.want)
		}
	}
}

func TestNormalizedAmount_Negative(t *testing.T) {
	got := normalizedAmount(big.NewInt(-1234), 2)
	if got != "-12.34" {
		t.Errorf("normalizedAmount(-1234, 2) = %q, want -12.34", got)
	}
}

func TestNormalizedAmount_Zero(t *testing.T) {
	if got := normalizedAmount(big.NewInt(0), 18); got != "0.000000000000000000" {
		t.Errorf("normalizedAmount(0, 18) = %q", got)
	}
}

func TestResolvePointer(t *testing.T) {
	doc, err := canonical.Parse([]byte(`{"":"empty","a/b":"slash","m~n":"tilde","arr":["x","y"],"s":"scalar"}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ptr  string
		want string
		ok   bool
	}{
		{"/", "empty", true},
		{"/a~1b", "slash", true},
		{"/m~0n", "tilde", true},
		{"/arr/0", "x", true},
		{"/arr/1", "y", true},
		{"/arr/01", "", false},
		{"/arr/+1", "", false},
		{"/arr/-0", "", false},
		{"/arr/-", "", false},
		{"/arr/10", "", false},
		{"/missing", "", false},
		{"/s/x", "", false},
		{"s", "", false},
	}
	for _, c := range cases {
		v, ok := resolvePointer(doc, c.ptr)
		if ok != c.ok || (ok && displayString(v) != c.want) {
			t.Errorf("resolvePointer(%q) = %v, %v; want %q, %v", c.ptr, v, ok, c.want, c.ok)
		}
	}
	if v, ok := resolvePointer(doc, ""); !ok || v != doc {
		t.Errorf(`resolvePointer("") must return the whole document`)
	}
}

func TestLegalFields(t *testing.T) {
	if got := legalFields(nil); got != nil {
		t.Fatalf("absent legal produced %v", got)
	}
	const sum = "a4244f1bab68afd39467ad8bfa7904024342d9758de5e7b0af41819a02347cbe"
	got := legalFields(&profile.Legal{
		Wrapper: "share", IssuerLEI: "5493000EXAMPLE0LEI67", HolderRights: "rights", MaturityDate: "2030-12-31",
		RedemptionTerms: &profile.DocRef{URI: "https://a/t", SHA256: sum},
		Rating:          &profile.Rating{Provider: "P", Grade: "AA", Ref: profile.DocRef{URI: "https://a/r", SHA256: sum}},
	})
	want := []ui.Field{
		{Label: "legal wrapper", Value: "share"},
		{Label: "legal issuerLei", Value: "5493000EXAMPLE0LEI67"},
		{Label: "legal holderRights", Value: "rights"},
		{Label: "legal redemptionTerms", Value: "uri:https://a/t sha256:" + sum},
		{Label: "legal maturityDate", Value: "2030-12-31"},
		{Label: "legal rating", Value: "provider:P grade:AA uri:https://a/r sha256:" + sum},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if n := len(legalFields(&profile.Legal{Wrapper: "claim"})); n != 1 {
		t.Fatalf("wrapper-only produced %d lines", n)
	}
}
