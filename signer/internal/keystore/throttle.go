package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// throttleState is the on-disk state that rate-limits repeated failed unlocks. The
// signer is a CLI started fresh for each attempt and has no in-memory counter to carry
// between tries, so the state lives next to the keystore (path + ".throttle.json").
// Otherwise an attacker could re-run the binary in a fast loop at no cost.
type throttleState struct {
	Failures        int   `json:"failures"`
	LockedUntilUnix int64 `json:"lockedUntilUnix"`
}

const (
	// throttleFreeAttempts is the number of consecutive failures allowed before a
	// lockout starts, so an operator who mistypes a password once or twice is not locked
	// out of their own keystore
	throttleFreeAttempts = 3
	throttleBase         = 5 * time.Second
	throttleMax          = 1 * time.Hour
	// throttleMaxShift caps the exponent used to compute the backoff, so a very large
	// failure count cannot overflow the shift; throttleMax is reached well before it
	throttleMaxShift = 20
)

func throttlePath(keystorePath string) string { return keystorePath + ".throttle.json" }

func loadThrottleState(keystorePath string) throttleState {
	data, err := os.ReadFile(throttlePath(keystorePath))
	if err != nil {
		// A missing or unreadable file means no failures on record, so a fresh keystore,
		// or one whose throttle file the operator deleted, is usable immediately
		return throttleState{}
	}
	var s throttleState
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt file counts as past the free attempts and starts a short lockout, so
		// corrupting or truncating the file does not reset a lockout
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

// CheckThrottle returns an error if keystorePath is locked out after failed unlocks.
// Call it before decrypting, so no attempt is made during a lockout and a wrong
// password cannot extend or shorten it.
func CheckThrottle(keystorePath string) error {
	s := loadThrottleState(keystorePath)
	if until := time.Unix(s.LockedUntilUnix, 0); s.LockedUntilUnix > 0 && time.Now().Before(until) {
		return fmt.Errorf("keystore: too many failed unlock attempts (%d); locked out for %s (until %s)", s.Failures, time.Until(until).Round(time.Second), until.UTC().Format(time.RFC3339))
	}
	return nil
}

// RecordUnlockFailure increments the failure counter of keystorePath and, once
// throttleFreeAttempts is exceeded, sets a lockout that doubles with each further
// failure up to throttleMax
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
