package textsafe

import (
	"strings"
	"testing"
)

func TestSanitizeForTerminal_LeavesPrintableTextAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"gram",
		"0xAbC123",
		"1.500000 USD",
		"MINT — THIS WILL INCREASE TOKEN SUPPLY",
		"Bar serial number (999.9 fine)",
		"日本語",
	} {
		if got := SanitizeForTerminal(s); got != s {
			t.Errorf("SanitizeForTerminal(%q) = %q, want it unchanged", s, got)
		}
		if !IsPrintable(s) {
			t.Errorf("IsPrintable(%q) = false, want true", s)
		}
	}
}

// TestSanitizeForTerminal_NeutralizesTerminalControls covers each way a
// package-supplied value could take control of the auditor's terminal: a
// carriage return rewinding the current line, a newline forging an extra one,
// an ANSI cursor-up sequence rewriting earlier lines, plus DEL and C1 bytes.
func TestSanitizeForTerminal_NeutralizesTerminalControls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"carriage return", "USD\r  amount : 1.5", `USD\x0d  amount : 1.5`},
		{"newline injection", "USD\n  vault : 0xdead", `USD\x0a  vault : 0xdead`},
		{"ansi cursor up", "USD\x1b[3A\x1b[2K", `USD\x1b[3A\x1b[2K`},
		{"ansi color", "\x1b[31mDANGER\x1b[0m", `\x1b[31mDANGER\x1b[0m`},
		{"del", "USD\x7f", `USD\x7f`},
		{"c1 next line", "USD\u0085x", `USD\x85x`},
		{"tab", "a\tb", `a\x09b`},
		{"bidi override", "gram\u202e", `gram\u202e`},
		{"invalid utf-8", "gram\xff", `gram\xff`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeForTerminal(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeForTerminal(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if IsPrintable(tc.in) {
				t.Errorf("IsPrintable(%q) = true, want false", tc.in)
			}
			// Whatever the exact escaping, no byte the terminal acts on may
			// survive, and the result must stay a single line.
			for _, b := range []string{"\x1b", "\r", "\n", "\x7f"} {
				if strings.Contains(got, b) {
					t.Errorf("SanitizeForTerminal(%q) = %q still contains raw %q", tc.in, got, b)
				}
			}
			if !IsPrintable(got) {
				t.Errorf("SanitizeForTerminal(%q) = %q is itself not printable", tc.in, got)
			}
		})
	}
}

// TestSanitizeForTerminal_EscapesEveryC0AndC1 is the exhaustive version: no
// byte in either control block may pass through, however it is rendered.
func TestSanitizeForTerminal_EscapesEveryC0AndC1(t *testing.T) {
	for r := rune(0); r <= 0x9f; r++ {
		if r >= 0x20 && r < 0x7f {
			continue // printable ASCII
		}
		in := "a" + string(r) + "b"
		got := SanitizeForTerminal(in)
		if strings.ContainsRune(got, r) {
			t.Errorf("SanitizeForTerminal(%q) = %q still contains U+%04X", in, got, r)
		}
		if IsPrintable(in) {
			t.Errorf("IsPrintable(%q) = true for control U+%04X, want false", in, r)
		}
	}
}
