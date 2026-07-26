package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// throttleState is the on-disk state used to rate-limit repeated failed
// keystore-unlock attempts. The signer is a stateless CLI invoked fresh
// per attempt —
// unlike a long-running daemon, it has no in-memory counter to carry
// between tries — so this state is persisted next to the keystore file
// (path+".throttle.json") rather than kept in memory, or an attacker could
// simply re-run the binary in a fast loop at no cost.
type throttleState struct {
	Failures        int   `json:"failures"`
	LockedUntilUnix int64 `json:"lockedUntilUnix"`
}

const (
	// throttleFreeAttempts is how many consecutive failures are allowed
	// before any lockout begins: an honest operator mistyping a password
	// once or twice must not be locked out of their own keystore.
	throttleFreeAttempts = 3
	throttleBase         = 5 * time.Second
	throttleMax          = 1 * time.Hour
	// throttleMaxShift caps the exponent used to compute the backoff
	// duration, so a very large failure count cannot overflow the shift —
	// throttleMax is always reached well before this bound.
	throttleMaxShift = 20
)

func throttlePath(keystorePath string) string { return keystorePath + ".throttle.json" }

func loadThrottleState(keystorePath string) throttleState {
	data, err := os.ReadFile(throttlePath(keystorePath))
	if err != nil {
		// Missing (or unreadable) throttle state means no prior failures
		// on record: fail open toward "no lockout yet", never toward
		// "already locked", so a fresh keystore or a throttle file an
		// operator deliberately cleared is usable immediately.
		return throttleState{}
	}
	var s throttleState
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupted throttle file fails closed (toward *more*
		// throttling, not less): treat it as already past the free-attempt
		// ceiling rather than silently resetting an attacker's lockout by
		// truncating or corrupting the file.
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

// CheckThrottle returns an error if keystorePath is currently locked out
// due to prior failed-unlock attempts. Call it before attempting to
// decrypt; a locked-out keystore must not even be tried, so a wrong
// password never resets or shortens an active lockout.
func CheckThrottle(keystorePath string) error {
	s := loadThrottleState(keystorePath)
	if until := time.Unix(s.LockedUntilUnix, 0); s.LockedUntilUnix > 0 && time.Now().Before(until) {
		return fmt.Errorf("keystore: too many failed unlock attempts (%d); locked out for %s (until %s)", s.Failures, time.Until(until).Round(time.Second), until.UTC().Format(time.RFC3339))
	}
	return nil
}

// RecordUnlockFailure increments keystorePath's failure counter and, once
// throttleFreeAttempts is exceeded, sets an exponentially growing lockout
// (capped at throttleMax) before the keystore may be tried again.
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

// RecordUnlockSuccess clears keystorePath's failure/lockout state.
func RecordUnlockSuccess(keystorePath string) error {
	if err := os.Remove(throttlePath(keystorePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keystore: clearing throttle state: %w", err)
	}
	return nil
}
