package auditpkg

import "strconv"

// hasLoneSurrogateEscape detects unpaired \uD800-\uDFFF escapes in JSON strings, which
// RFC 7493 section 2.1 forbids. Decoding them as U+FFFD would erase distinctions between
// inputs, so reject them before parsing. Tracking strings and escaped backslashes keeps
// literal "ud800" text from being mistaken for an escape. Other malformed input is left
// to the JSON decoder.
func hasLoneSurrogateEscape(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			inString = c == '"'
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if i+1 < len(raw) && raw[i+1] == 'u' {
				u, ok := hex4(raw, i+2)
				switch {
				case !ok:
				case u >= 0xDC00 && u <= 0xDFFF:
					return true
				case u >= 0xD800 && u <= 0xDBFF:
					lo, paired := uint32(0), false
					if i+11 < len(raw) && raw[i+6] == '\\' && raw[i+7] == 'u' {
						lo, paired = hex4(raw, i+8)
					}
					if !paired || lo < 0xDC00 || lo > 0xDFFF {
						return true
					}
					i += 6
				}
				i += 4
			}
			i++
		}
	}
	return false
}

// hex4 reads four hex digits at raw[at:]
func hex4(raw []byte, at int) (uint32, bool) {
	if at+4 > len(raw) {
		return 0, false
	}
	u, err := strconv.ParseUint(string(raw[at:at+4]), 16, 32)
	return uint32(u), err == nil
}
