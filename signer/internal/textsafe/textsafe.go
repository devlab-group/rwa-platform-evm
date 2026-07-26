// Package textsafe neutralizes untrusted text before it is printed to the
// auditor's terminal, and answers whether a string is safe to accept at all.
//
// The offline review screen is the auditor's only control: they read it, then
// sign an opaque 32-byte digest. Most of what it renders comes from the .rwa
// package, which is untrusted -- a display value, a token unit, a proof type.
// A terminal treats some of those bytes as commands rather than text: a
// carriage return rewinds to the start of the line, ESC [ 2 A moves the cursor
// up two lines, and either one lets a crafted field overwrite the amount, the
// vault, or the digest the auditor is checking. So the auditor would read one
// attestation and sign another.
//
// Nothing here strips characters silently: an offending byte becomes a visible
// \xNN escape. A value that tried to move the cursor should look wrong on
// screen, not merely be defused.
package textsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeForTerminal returns s with every rune a terminal would act on
// rather than draw replaced by a visible escape (\xNN, or \uNNNN above
// U+00FF). Printable text, including non-ASCII, is returned unchanged.
//
// Escaping the ESC byte itself is what disarms an ANSI sequence: with the
// introducer rendered as the four characters \x1b, the rest of a CSI (or OSC,
// or any other) sequence is ordinary printable text that the terminal draws
// instead of interpreting, and the auditor sees exactly the bytes that were
// in the package.
//
// The result is always a single line: newlines are escaped like any other
// control character, so a value can never add or displace a row of the review
// screen. Callers pass field values and labels through here, never the format
// string's own layout.
func SanitizeForTerminal(s string) string {
	if IsPrintable(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Not valid UTF-8; escape the raw byte rather than emit a
			// replacement char that hides what was actually there.
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

// IsPrintable reports whether every rune of s is valid UTF-8 and printable,
// i.e. whether SanitizeForTerminal would return s unchanged. Validators use it
// to reject a control character at parse time, before it ever reaches a
// terminal; sanitizing at render time stays in place regardless, since free-form
// values such as an asset's displayFields cannot be constrained this way.
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

// printableRune reports whether r draws as a glyph. The explicit C0/DEL/C1
// range check is redundant with unicode.IsPrint but states the terminal-control
// bytes this exists for. unicode.IsPrint additionally excludes tabs (which
// distort a column layout), the line/paragraph separators U+2028 and U+2029,
// and the Cf formatting characters -- among them the bidi overrides that can
// reverse how a value reads on screen without changing a byte of it.
func printableRune(r rune) bool {
	if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
		return false
	}
	return unicode.IsPrint(r)
}
