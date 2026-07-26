package keystore

import (
	"fmt"
	"strings"
)

// MinPasswordLength is the minimum length this signer accepts for a
// keystore password it creates (CreateNative) or that ValidatePassword is
// otherwise asked to check. NIST SP 800-63B recommends weighting length
// over forced composition rules, so this is a length floor plus a small
// weak-password blocklist rather than an upper/lower/digit/symbol
// requirement.
const MinPasswordLength = 12

// commonWeakPasswords is a short blocklist of passwords that are trivially
// guessable regardless of length. It is deliberately small: this is an
// offline, air-gapped tool with no network access to a live
// breached-password corpus, so exhaustive list-checking is out of scope —
// but rejecting the most obvious placeholders (including this repo's own
// go-ethereum test passphrase, which must never be reused for a real key)
// meaningfully raises the floor.
var commonWeakPasswords = map[string]bool{
	"password":                     true,
	"passw0rd1234":                 true,
	"correct horse battery staple": true, // xkcd's example password is not a secret
	"12345678901234567890":         true,
	"qwertyuiopasdfghjkl":          true,
	"changeme123456":               true,
	"letmein123456":                true,
}

// ValidatePassword returns a descriptive error if password does not meet
// this signer's minimum keystore-password policy. It is enforced only when
// this signer itself creates a keystore (CreateNative, and therefore
// "signer keystore create"/"import") — it cannot and does not
// retroactively apply to an existing external keystore that Load is asked
// to unlock, since the signer has no way to change a password it did not
// choose. It never logs or wraps password into the returned error.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("keystore: password must be at least %d characters (got %d)", MinPasswordLength, len(password))
	}
	if commonWeakPasswords[strings.ToLower(password)] {
		return fmt.Errorf("keystore: password is a known weak/placeholder value; choose a different one")
	}
	if lowEntropy(password) {
		return fmt.Errorf("keystore: password uses too few distinct characters (e.g. a repeated run) to be secure")
	}
	return nil
}

// lowEntropy rejects passwords built from a very small character alphabet
// repeated to reach the length floor (e.g. "aaaaaaaaaaaa" or
// "ababababbaba"), which the length check alone would not catch.
func lowEntropy(password string) bool {
	seen := make(map[rune]bool)
	for _, r := range password {
		seen[r] = true
	}
	return len(seen) < 6
}
