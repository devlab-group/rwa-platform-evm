package keystore

import "testing"

func TestValidatePassword_RejectsTooShort(t *testing.T) {
	if err := ValidatePassword("short1pass"); err == nil { // 10 chars, below the 12 floor
		t.Fatal("expected rejection of a too-short password")
	}
}

func TestValidatePassword_RejectsKnownWeakValues(t *testing.T) {
	for _, pw := range []string{
		"password",
		"correct horse battery staple",
		"CORRECT HORSE BATTERY STAPLE", // case-insensitive
	} {
		if err := ValidatePassword(pw); err == nil {
			t.Errorf("expected rejection of known-weak password %q", pw)
		}
	}
}

func TestValidatePassword_RejectsLowEntropyRepeats(t *testing.T) {
	if err := ValidatePassword("aaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("expected rejection of a password with too few distinct characters")
	}
}

func TestValidatePassword_AcceptsAReasonablePassword(t *testing.T) {
	if err := ValidatePassword("Tr0ub4dor&Zebra-Canyon-99"); err != nil {
		t.Errorf("expected a strong password to be accepted: %v", err)
	}
}
