package keystore

import (
	"fmt"
	"strings"
)

// MinPasswordLength is the minimum length, in bytes, of a password for a keystore this
// signer creates. NIST SP 800-63B recommends weighting length over composition rules,
// so the policy is a length floor, a distinct-character floor and a small blocklist
// instead of upper-case, digit or symbol requirements.
const MinPasswordLength = 12

// commonWeakPasswords is a short blocklist of trivially guessable passwords. It is small
// because the signer is offline and has no breached-password corpus to check against,
// but it still rejects the most obvious placeholders.
var commonWeakPasswords = map[string]bool{
	"password":                     true,
	"passw0rd1234":                 true,
	"correct horse battery staple": true, // xkcd's example password is not a secret
	"12345678901234567890":         true,
	"qwertyuiopasdfghjkl":          true,
	"changeme123456":               true,
	"letmein123456":                true,
}

// ValidatePassword requires MinPasswordLength bytes, six distinct characters
// and no case-insensitive blocklist match. CreateNative enforces it for keystore
// create/import; Load does not reject an existing keystore's password. Errors
// never include the password.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("keystore: password must be at least %d bytes (got %d)", MinPasswordLength, len(password))
	}
	if commonWeakPasswords[strings.ToLower(password)] {
		return fmt.Errorf("keystore: password is a known weak/placeholder value; choose a different one")
	}
	if lowEntropy(password) {
		return fmt.Errorf("keystore: password uses too few distinct characters (e.g. a repeated run) to be secure")
	}
	return nil
}

// lowEntropy rejects passwords built from a very small alphabet repeated to reach the
// length floor, such as "aaaaaaaaaaaa" or "ababababbaba", which the length check alone
// would not catch
func lowEntropy(password string) bool {
	seen := make(map[rune]bool)
	for _, r := range password {
		seen[r] = true
	}
	return len(seen) < 6
}
