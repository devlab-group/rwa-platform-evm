package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// throttleState persists failures in <keystore>.throttle.json so restarting the CLI
// does not reset its counter
type throttleState struct {
	Failures        int   `json:"failures"`
	LockedUntilUnix int64 `json:"lockedUntilUnix"`
}

const (
	// throttleFreeAttempts allows mistyped passwords before starting a lockout
	throttleFreeAttempts = 3
	throttleBase         = 5 * time.Second
	throttleMax          = 1 * time.Hour
	// throttleMaxShift prevents backoff-shift overflow; throttleMax caps the delay earlier
	throttleMaxShift = 20
)

func throttlePath(keystorePath string) string { return keystorePath + ".throttle.json" }

func loadThrottleState(keystorePath string) throttleState {
	data, err := os.ReadFile(throttlePath(keystorePath))
	if err != nil {
		// Missing or unreadable state returns zero failures; deletion bypasses the local
		// counter
		return throttleState{}
	}
	var s throttleState
	if err := json.Unmarshal(data, &s); err != nil {
		// Malformed JSON creates a fresh throttleBase lockout on every read
		return throttleState{Failures: throttleFreeAttempts + 1, LockedUntilUnix: time.Now().Add(throttleBase).Unix()}
	}
	return s
}

func saveThrottleState(keystorePath string, s throttleState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("keystore: encoding throttle state: %w", err)
	}
	if err := os.WriteFile(throttlePath(keystorePath), data, 0o600); err != nil {
		return fmt.Errorf("keystore: writing throttle state: %w", err)
	}
	return nil
}

// CheckThrottle rejects an active lockout; call before password reading/decryption so
// denied attempts do not update ordinary failure state
func CheckThrottle(keystorePath string) error {
	s := loadThrottleState(keystorePath)
	if until := time.Unix(s.LockedUntilUnix, 0); s.LockedUntilUnix > 0 && time.Now().Before(until) {
		return fmt.Errorf("keystore: too many failed unlock attempts (%d); locked out for %s (until %s)", s.Failures, time.Until(until).Round(time.Second), until.UTC().Format(time.RFC3339))
	}
	return nil
}

// RecordUnlockFailure increments persisted failures and, beyond throttleFreeAttempts,
// doubles lockout duration per failure up to throttleMax
func RecordUnlockFailure(keystorePath string) error {
	s := loadThrottleState(keystorePath)
	s.Failures++
	if s.Failures > throttleFreeAttempts {
		shift := s.Failures - throttleFreeAttempts - 1
		if shift > throttleMaxShift {
			shift = throttleMaxShift
		}
		backoff := throttleBase << uint(shift)
		if backoff <= 0 || backoff > throttleMax {
			backoff = throttleMax
		}
		s.LockedUntilUnix = time.Now().Add(backoff).Unix()
	}
	return saveThrottleState(keystorePath, s)
}

// RecordUnlockSuccess clears the failure and lockout state of keystorePath
func RecordUnlockSuccess(keystorePath string) error {
	if err := os.Remove(throttlePath(keystorePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keystore: clearing throttle state: %w", err)
	}
	return nil
}
