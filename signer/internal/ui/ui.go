// Package ui renders the offline review screen and reads the auditor's
// explicit y/N confirmation before signing.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/rwa-platform/signer/internal/textsafe"
)

// Field is one rendered line of the review screen: a label and its value.
// Warn marks a field that needs the auditor's extra attention (e.g. a proof
// hash with no attached file).
type Field struct {
	Label string
	Value string
	Warn  bool
}

// Render prints a labeled review screen to w: first the security-critical
// typed-data fields, then the profile's displayFields, then any warnings.
//
// Every label, value and title printed here is passed through
// textsafe.SanitizeForTerminal first. Most of them originate in the untrusted
// .rwa package, and a value that could emit a carriage return or a cursor-up
// escape would be able to rewrite the amount or digest line the auditor is
// checking -- see the package comment on textsafe.
func Render(w io.Writer, title string, critical, display []Field) {
	fmt.Fprintln(w, "==============================================================")
	fmt.Fprintln(w, textsafe.SanitizeForTerminal(title))
	fmt.Fprintln(w, "==============================================================")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Security-critical fields (verify these against your own records):")
	renderFields(w, critical)
	if len(display) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Asset display fields:")
		renderFields(w, display)
	}
	fmt.Fprintln(w)
}

func renderFields(w io.Writer, fields []Field) {
	// Sanitize labels before measuring them, so column alignment is computed
	// from what actually gets printed. Measured in runes because fmt's %-*s
	// pads by rune count too — measuring bytes would over-pad any label with
	// a non-ASCII character and skew the ':' column.
	labels := make([]string, len(fields))
	width := 0
	for i, f := range fields {
		labels[i] = textsafe.SanitizeForTerminal(f.Label)
		if n := utf8.RuneCountInString(labels[i]); n > width {
			width = n
		}
	}
	for i, f := range fields {
		marker := "  "
		if f.Warn {
			marker = "! "
		}
		fmt.Fprintf(w, "%s%-*s : %s\n", marker, width, labels[i], textsafe.SanitizeForTerminal(f.Value))
	}
}

// RenderActionBanner prints a large, unmistakable banner identifying
// primaryType before the review screen, so a mint attestation can't be
// mistaken for a burn attestation. The mint and
// burn banners are built to be confusable neither at a glance nor by
// substring: different border glyphs, different wording with no shared
// word beyond "TOKEN", and an explicit statement of the on-chain effect
// (supply increases vs. decreases), not just the type name -- an auditor
// skimming past "MintAttestation" vs. "BurnAttestation" in small print is
// exactly the failure mode this exists to prevent.
func RenderActionBanner(w io.Writer, primaryType string) {
	switch primaryType {
	case "MintAttestation":
		printBanner(w, '+', []string{
			"MINT — THIS WILL INCREASE TOKEN SUPPLY",
			"Tokens will be created into the Vault.",
		})
	case "BurnAttestation":
		printBanner(w, '#', []string{
			"BURN — THIS WILL DECREASE TOKEN SUPPLY",
			"Tokens will be destroyed from the Vault.",
		})
	default:
		printBanner(w, '?', []string{"UNRECOGNIZED ACTION TYPE: " + primaryType})
	}
}

func printBanner(w io.Writer, border rune, lines []string) {
	// The unrecognized-type banner embeds the package's own primaryType
	// string, so banner lines get the same treatment as field values.
	lines = append([]string(nil), lines...)
	width := 0
	for i, l := range lines {
		lines[i] = textsafe.SanitizeForTerminal(l)
		if len(lines[i]) > width {
			width = len(lines[i])
		}
	}
	width += 4
	bar := strings.Repeat(string(border), width)
	fmt.Fprintln(w, bar)
	for _, l := range lines {
		fmt.Fprintf(w, "%c %-*s%c\n", border, width-3, l, border)
	}
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
}

// Confirm prints prompt and reads a line from r, returning true only if the
// auditor explicitly typed "y" or "yes" (case-insensitive). Anything else,
// including a blank line, is treated as "no" (fail closed).
func Confirm(w io.Writer, r io.Reader, prompt string) (bool, error) {
	fmt.Fprintf(w, "%s [y/N]: ", textsafe.SanitizeForTerminal(prompt))
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("ui: reading confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
