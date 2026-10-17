package keystore

import (
	"os"
	"path/filepath"
	"strings"
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
	// Exactly throttleFreeAttempts failures recorded; still not locked
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

	// pathA gets exactly one failure past the free budget; pathB gets several
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

// A failure to clear or save throttle state is reported, because the signer refuses to
// release a signature when its bookkeeping did not complete
func TestThrottle_StateIOFailuresAreReported(t *testing.T) {
	t.Run("clearing fails when the state path cannot be removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auditor.json")
		stuck := throttlePath(path)
		if err := os.MkdirAll(filepath.Join(stuck, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := RecordUnlockSuccess(path)
		if err == nil || !strings.Contains(err.Error(), "clearing throttle state") {
			t.Errorf("RecordUnlockSuccess error = %v, want a clearing error", err)
		}
	})
	t.Run("clearing a missing state file is not an error", func(t *testing.T) {
		if err := RecordUnlockSuccess(filepath.Join(t.TempDir(), "auditor.json")); err != nil {
			t.Errorf("RecordUnlockSuccess on a clean keystore: %v", err)
		}
	})
	t.Run("saving fails when the directory is missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "no-such-dir", "auditor.json")
		err := RecordUnlockFailure(path)
		if err == nil || !strings.Contains(err.Error(), "writing throttle state") {
			t.Errorf("RecordUnlockFailure error = %v, want a writing error", err)
		}
	})
}
