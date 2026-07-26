package keystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThrottle_FreeAttemptsDoNotLockOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	for i := 0; i < throttleFreeAttempts; i++ {
		if err := CheckThrottle(path); err != nil {
			t.Fatalf("attempt %d: CheckThrottle: %v", i, err)
		}
		if err := RecordUnlockFailure(path); err != nil {
			t.Fatalf("attempt %d: RecordUnlockFailure: %v", i, err)
		}
	}
	// Exactly throttleFreeAttempts failures recorded; still not locked.
	if err := CheckThrottle(path); err != nil {
		t.Errorf("expected no lockout after exactly %d failures, got: %v", throttleFreeAttempts, err)
	}
}

func TestThrottle_LocksOutAfterFreeAttemptsExceeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	for i := 0; i < throttleFreeAttempts+1; i++ {
		if err := RecordUnlockFailure(path); err != nil {
			t.Fatalf("RecordUnlockFailure: %v", err)
		}
	}
	if err := CheckThrottle(path); err == nil {
		t.Fatal("expected a lockout after exceeding the free-attempt budget")
	}
}

func TestThrottle_BackoffGrowsWithRepeatedFailures(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "a.json")
	pathB := filepath.Join(t.TempDir(), "b.json")

	// pathA gets exactly one failure past the free budget; pathB gets several.
	for i := 0; i < throttleFreeAttempts+1; i++ {
		RecordUnlockFailure(pathA)
	}
	for i := 0; i < throttleFreeAttempts+4; i++ {
		RecordUnlockFailure(pathB)
	}

	sa := loadThrottleState(pathA)
	sb := loadThrottleState(pathB)
	if sb.LockedUntilUnix <= sa.LockedUntilUnix {
		t.Errorf("expected more repeated failures to produce a longer or equal-capped lockout: a=%d b=%d", sa.LockedUntilUnix, sb.LockedUntilUnix)
	}
}

func TestThrottle_SuccessClearsLockout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	for i := 0; i < throttleFreeAttempts+2; i++ {
		RecordUnlockFailure(path)
	}
	if err := CheckThrottle(path); err == nil {
		t.Fatal("expected a lockout before RecordUnlockSuccess")
	}
	if err := RecordUnlockSuccess(path); err != nil {
		t.Fatalf("RecordUnlockSuccess: %v", err)
	}
	if err := CheckThrottle(path); err != nil {
		t.Errorf("expected the lockout to be cleared after a recorded success, got: %v", err)
	}
}

func TestThrottle_UnknownKeystoreHasNoLockout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-touched.json")
	if err := CheckThrottle(path); err != nil {
		t.Errorf("expected no lockout for a keystore with no throttle history, got: %v", err)
	}
}

func TestThrottle_CorruptedStateFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	if err := os.WriteFile(throttlePath(path), []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("writing corrupted throttle state: %v", err)
	}
	if err := CheckThrottle(path); err == nil {
		t.Error("expected a corrupted throttle file to fail closed (locked), not open")
	}
}
