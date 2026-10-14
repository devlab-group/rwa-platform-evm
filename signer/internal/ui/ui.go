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

// Render prints a labeled review screen to w: the security-critical fields, then the
// profile's displayFields. A field with Warn set gets a leading "!".
//
// Every title, label and value passes through textsafe.SanitizeForTerminal. Most come
// from the untrusted .rwa package, and a value that could emit a carriage return or a
// cursor-up escape could rewrite the amount or digest line the auditor is checking.
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
	// Measure the sanitized labels, in runes: fmt's %-*s pads by rune count, so
	// measuring bytes would over-pad a label with a non-ASCII character and skew the
	// ':' column
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

// RenderActionBanner prints a banner naming primaryType before the review screen, so a
// mint cannot be mistaken for a burn. The two banners use different border glyphs and
// different leading words, and state the on-chain effect (supply increases or
// decreases) instead of relying on "MintAttestation" versus "BurnAttestation" in small
// print.
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
	// The unrecognized-type banner embeds the package's own primaryType string, so
	// banner lines get the same treatment as field values
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

// Confirm prints prompt and reads a line from r. It returns true only if the auditor
// typed "y" or "yes" (case-insensitive, surrounding whitespace ignored). Anything
// else, including a blank line or EOF with no input, returns false.
func Confirm(w io.Writer, r io.Reader, prompt string) (bool, error) {
	fmt.Fprintf(w, "%s [y/N]: ", textsafe.SanitizeForTerminal(prompt))
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("ui: reading confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
