// Package textsafe validates printable text and escapes untrusted values for
// the auditor's terminal. Review fields from a .rwa package can contain cursor
// controls that overwrite an amount, Vault or digest on screen while leaving
// the signed 32-byte digest unchanged.
//
// Control characters become visible escapes rather than disappearing, exposing
// attempted terminal manipulation in the review.
package textsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeForTerminal preserves printable text, including non-ASCII, and
// escapes other runes as \xNN through U+00FF or \u with at least four hex digits.
// Escaping ESC as the literal \x1b neutralizes ANSI CSI, OSC and other sequences.
// Newlines are escaped too, keeping each value on one line. Callers sanitize
// labels and values, leaving the review format's own layout intact.
func SanitizeForTerminal(s string) string {
	if IsPrintable(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Not valid UTF-8; escape the raw byte rather than emit a replacement
			// character that hides what was there
			fmt.Fprintf(&b, `\x%02x`, s[i])
			i++
			continue
		}
		if printableRune(r) {
			b.WriteRune(r)
		} else if r <= 0xff {
			fmt.Fprintf(&b, `\x%02x`, r)
		} else {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += size
	}
	return b.String()
}

// IsPrintable reports whether every rune of s is valid UTF-8 and printable, that is,
// whether SanitizeForTerminal would return s unchanged. Validators use it to reject a
// control character at parse time, before it reaches a terminal. Render-time
// sanitizing still applies, because free-form values such as the asset fields shown
// through displayFields cannot be constrained this way.
func IsPrintable(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}
		if !printableRune(r) {
			return false
		}
		i += size
	}
	return true
}

// printableRune reports whether r draws as a glyph. The explicit C0, DEL and C1 range
// check overlaps unicode.IsPrint but names the terminal-control bytes this package
// exists for. unicode.IsPrint also excludes tabs (which distort the column layout),
// the line and paragraph separators U+2028 and U+2029, non-ASCII spaces, and the Cf
// formatting characters, including the bidi overrides that can reverse how a value
// reads on screen without changing a byte of it.
func printableRune(r rune) bool {
	if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
		return false
	}
	return unicode.IsPrint(r)
}
