package keystore

import (
	"fmt"
	"strings"
)

// MinPasswordLength is the byte-length floor for newly created keystores;
// ValidatePassword also checks distinct characters and a blocklist
const MinPasswordLength = 12

// commonWeakPasswords is a small local blocklist, used without an online
// breached-password lookup
var commonWeakPasswords = map[string]bool{
	"password":                     true,
	"passw0rd1234":                 true,
	"correct horse battery staple": true, // xkcd's example password is not a secret
	"12345678901234567890":         true,
	"qwertyuiopasdfghjkl":          true,
	"changeme123456":               true,
	"letmein123456":                true,
}

// ValidatePassword requires MinPasswordLength bytes, six distinct runes and no
// case-insensitive blocklist match; errors omit the password. CreateNative enforces it
// for create/import, while Load accepts existing passwords.
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

// lowEntropy rejects fewer than six distinct runes, even when repetition meets the
// length floor
func lowEntropy(password string) bool {
	seen := make(map[rune]bool)
	for _, r := range password {
		seen[r] = true
	}
	return len(seen) < 6
}
