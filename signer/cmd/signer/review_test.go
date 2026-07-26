package main

import (
	"math/big"
	"testing"
)

// TestNormalizedAmount_MisleadingDecimals guards the misleading-decimals
// scenario: the same raw smallest-unit amount reads as wildly different
// real-world quantities depending on tokenDecimals, so the confirmation
// screen has to show the normalized value rather than making the auditor do
// that arithmetic (and trust it) themselves.
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
