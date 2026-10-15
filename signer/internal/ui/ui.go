// Package ui renders review screens and reads explicit y/N confirmation before signing
package ui

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/rwa-platform/signer/internal/textsafe"
)

// Field holds a review label/value; Warn flags extra attention, such as a proof hash
// without an attachment
type Field struct {
	Label string
	Value string
	Warn  bool
}

// Render prints critical fields before profile display fields, marking Warn with "!".
// textsafe.SanitizeForTerminal escapes titles, labels and values to prevent
// control-driven repainting.
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
	// Measure sanitized labels in runes to match fmt's %-*s padding; byte counts over-pad
	// multibyte labels
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

// RenderActionBanner distinguishes mint/burn with action words, supply
// increase/decrease and different borders before field review
func RenderActionBanner(w io.Writer, primaryType string) {
	switch primaryType {
	case "MintAttestation":
		printBanner(w, '+', []string{
			"MINT - THIS WILL INCREASE TOKEN SUPPLY",
			"Tokens will be created into the Vault.",
		})
	case "BurnAttestation":
		printBanner(w, '#', []string{
			"BURN - THIS WILL DECREASE TOKEN SUPPLY",
			"Tokens will be destroyed from the Vault.",
		})
	default:
		printBanner(w, '?', []string{"UNRECOGNIZED ACTION TYPE: " + primaryType})
	}
}

func printBanner(w io.Writer, border rune, lines []string) {
	// Sanitize banner text too: unknown primaryType values come from the package
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

// Confirm accepts only trimmed, case-insensitive "y"/"yes"; blank, other answers or EOF
// without input decline. Non-EOF read failures return an error.
func Confirm(w io.Writer, r io.Reader, prompt string) (bool, error) {
	fmt.Fprintf(w, "%s [y/N]: ", textsafe.SanitizeForTerminal(prompt))
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("ui: reading confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
