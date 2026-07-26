package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirm_AcceptsYAndYes(t *testing.T) {
	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n", "  yes  \n"} {
		var out bytes.Buffer
		ok, err := Confirm(&out, strings.NewReader(in), "Sign?")
		if err != nil {
			t.Fatalf("Confirm(%q): %v", in, err)
		}
		if !ok {
			t.Errorf("Confirm(%q) = false, want true", in)
		}
	}
}

func TestConfirm_RejectsAnythingElse(t *testing.T) {
	for _, in := range []string{"n\n", "no\n", "\n", "maybe\n", ""} {
		var out bytes.Buffer
		ok, err := Confirm(&out, strings.NewReader(in), "Sign?")
		if err != nil {
			t.Fatalf("Confirm(%q): %v", in, err)
		}
		if ok {
			t.Errorf("Confirm(%q) = true, want false (fail closed)", in)
		}
	}
}

// TestRenderActionBanner_MintAndBurnAreDistinguishable checks that a mint
// banner can't be mistaken for a burn banner. The two must not merely differ
// somewhere -- each must independently and unambiguously say what it is, with
// no shared distinguishing word and no accidental overlap.
func TestRenderActionBanner_MintAndBurnAreDistinguishable(t *testing.T) {
	var mintOut, burnOut bytes.Buffer
	RenderActionBanner(&mintOut, "MintAttestation")
	RenderActionBanner(&burnOut, "BurnAttestation")
	mint, burn := mintOut.String(), burnOut.String()

	if mint == burn {
		t.Fatal("mint and burn banners are identical")
	}
	if !strings.Contains(mint, "MINT") || strings.Contains(mint, "BURN") {
		t.Errorf("mint banner does not clearly say MINT (and only MINT):\n%s", mint)
	}
	if !strings.Contains(burn, "BURN") || strings.Contains(burn, "MINT") {
		t.Errorf("burn banner does not clearly say BURN (and only BURN):\n%s", burn)
	}
	if !strings.Contains(mint, "INCREASE") {
		t.Errorf("mint banner does not state the supply effect:\n%s", mint)
	}
	if !strings.Contains(burn, "DECREASE") {
		t.Errorf("burn banner does not state the supply effect:\n%s", burn)
	}
	// The two banners must not even share a border glyph, so a terminal
	// scrollback skim catches the difference before reading any word.
	if mint[0] == burn[0] {
		t.Error("mint and burn banners use the same border glyph")
	}
}

func TestRender_IncludesFieldsAndWarnings(t *testing.T) {
	var out bytes.Buffer
	Render(&out, "Review MintAttestation", []Field{
		{Label: "chainId", Value: "31337"},
		{Label: "auditor", Value: "0xabc"},
	}, []Field{
		{Label: "Serial", Value: "12345"},
		{Label: "Proof file", Value: "missing (hash only)", Warn: true},
	})
	s := out.String()
	for _, want := range []string{"Review MintAttestation", "chainId", "31337", "Serial", "12345", "Proof file"} {
		if !strings.Contains(s, want) {
			t.Errorf("Render output missing %q\n---\n%s", want, s)
		}
	}
}

// TestRender_CraftedFieldCannotRewriteOtherLines is the review screen's core
// guarantee: what the auditor sees is what they sign. A tokenUnit is appended
// to the normalized-amount line and comes straight from the untrusted package,
// so it is the natural place to smuggle a carriage return or a cursor-up
// escape and repaint the amount, vault and digest lines with a smaller,
// friendlier attestation. None of it may reach the terminal.
func TestRender_CraftedFieldCannotRewriteOtherLines(t *testing.T) {
	craftedUnit := "USD\r  amount (normalized)  : 1.500000 USD\x1b[4A\x1b[2K"
	var out bytes.Buffer
	Render(&out, "Review MintAttestation — pkg.rwa", []Field{
		{Label: "vault", Value: "0x1111111111111111111111111111111111111111"},
		{Label: "amount (raw, smallest unit)", Value: "9000000000000000000000"},
		{Label: "amount (normalized)", Value: "9000.000000 " + craftedUnit},
		{Label: "EIP-712 digest", Value: "0xdeadbeef"},
	}, []Field{
		// displayFields values are free-form by design, so they get the same
		// treatment as the critical fields.
		{Label: "Serial\x1b[31m", Value: "12345\nSerial : 99999"},
	})
	s := out.String()

	for _, raw := range []string{"\x1b", "\r", "\x7f"} {
		if strings.Contains(s, raw) {
			t.Errorf("rendered screen contains raw %q:\n%s", raw, s)
		}
	}
	// Every field occupies exactly one line: an injected newline must not be
	// able to add a row, so the screen has the same shape it would have with
	// four critical fields and one display field of ordinary text.
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	var benign bytes.Buffer
	Render(&benign, "Review MintAttestation — pkg.rwa", []Field{
		{Label: "vault", Value: "0x1"},
		{Label: "amount (raw, smallest unit)", Value: "9"},
		{Label: "amount (normalized)", Value: "9 USD"},
		{Label: "EIP-712 digest", Value: "0xd"},
	}, []Field{{Label: "Serial", Value: "12345"}})
	wantLines := strings.Split(strings.TrimSuffix(benign.String(), "\n"), "\n")
	if len(lines) != len(wantLines) {
		t.Fatalf("crafted fields changed the screen from %d lines to %d:\n%s", len(wantLines), len(lines), s)
	}
	// The lines the auditor reads the attestation from must be byte-identical
	// to what their own field values say, with nothing appended or overwritten.
	for i, want := range map[int]string{
		5: "  vault                       : 0x1111111111111111111111111111111111111111",
		6: "  amount (raw, smallest unit) : 9000000000000000000000",
		8: "  EIP-712 digest              : 0xdeadbeef",
	} {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q\n---\n%s", i, lines[i], want, s)
		}
	}
	if !strings.HasPrefix(lines[7], "  amount (normalized)         : 9000.000000 ") {
		t.Errorf("amount line was rewritten: %q\n---\n%s", lines[7], s)
	}
	// The escaped bytes stay visible, so the auditor can see the value tried
	// to do something rather than the tool quietly cleaning it up.
	if !strings.Contains(s, `\x0d`) || !strings.Contains(s, `\x1b`) {
		t.Errorf("control bytes were dropped instead of shown as escapes:\n%s", s)
	}
}

func TestRenderActionBanner_SanitizesUnrecognizedType(t *testing.T) {
	var out bytes.Buffer
	RenderActionBanner(&out, "Mint\rBURN — THIS WILL DECREASE TOKEN SUPPLY")
	s := out.String()
	if strings.ContainsAny(s, "\r\x1b") {
		t.Errorf("banner passed through a raw control byte:\n%s", s)
	}
	if !strings.Contains(s, "UNRECOGNIZED ACTION TYPE") {
		t.Errorf("banner does not flag the unrecognized type:\n%s", s)
	}
}
