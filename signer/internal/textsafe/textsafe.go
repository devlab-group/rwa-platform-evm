// Package textsafe validates printable text and visibly escapes terminal controls.
// Malicious review text could repaint amounts, Vault or digests without changing signed
// bytes; visible escapes expose the manipulation.
package textsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeForTerminal preserves printable Unicode, escaping non-printable runes as \xNN
// through U+00FF or \u with at least four hex digits, and invalid UTF-8 as raw-byte
// escapes. Escaped ESC neutralizes ANSI sequences; escaped newlines keep values on one
// line. Callers sanitize labels/values while retaining layout.
func SanitizeForTerminal(s string) string {
	if IsPrintable(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Escape the invalid raw byte so replacement characters do not hide it
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

// IsPrintable requires valid UTF-8 and printableRune throughout; accepted text needs no
// sanitizing. Validators reject controls, while rendering sanitizes free-form fields.
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

// printableRune rejects C0, DEL and C1, then uses unicode.IsPrint. This also excludes
// tabs, U+2028/U+2029, non-ASCII spaces and Cf formatting, including bidi overrides.
func printableRune(r rune) bool {
	if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
		return false
	}
	return unicode.IsPrint(r)
}
