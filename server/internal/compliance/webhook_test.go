package compliance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

const testSecret = "webhook-secret"

func sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// validPayload supplies required fields and a fresh occurredAt; tests override the
// case-specific value
func validPayload(eventID, address, status string) WebhookPayload {
	return WebhookPayload{
		EventID: eventID, Address: address, Provider: "test-provider", Status: status,
		OccurredAt: time.Now().UTC().Unix(),
	}
}

func TestVerifyHMAC(t *testing.T) {
	payload := []byte(`{"a":1}`)
	sig := sign(payload, testSecret)
	if !VerifyHMAC(payload, sig, testSecret) {
		t.Error("expected valid signature to verify")
	}
	if VerifyHMAC(payload, sig, "wrong-secret") {
		t.Error("expected signature to fail with wrong secret")
	}
	if VerifyHMAC([]byte(`{"a":2}`), sig, testSecret) {
		t.Error("expected signature to fail for tampered payload")
	}
}

func TestWebhookProcessAcceptsVerifiedOwnership(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A1"
	if err := investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true, UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-1", addr, "Allowed"))
	sig := sign(payload, testSecret)

	wp, err := svc.Process(context.Background(), payload, sig, false)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if wp.Address != addr || wp.Status != "Allowed" {
		t.Errorf("got %+v", wp)
	}
}

// TestVerifyHMACRejectsEmptySecret checks refusal of signatures anyone could forge for
// an unconfigured webhook
func TestVerifyHMACRejectsEmptySecret(t *testing.T) {
	payload := []byte(`{"address":"0xattacker","status":"Allowed"}`)
	forgedSig := sign(payload, "") // exactly what an attacker who knows the secret is empty would compute
	if VerifyHMAC(payload, forgedSig, "") {
		t.Fatal("VerifyHMAC accepted a signature computed with an empty secret")
	}
}

// TestWebhookProcessRejectsEmptySecret is the same check at the Process layer,
// for a WebhookService built with an empty secret
func TestWebhookProcessRejectsEmptySecret(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, "")
	payload, _ := json.Marshal(validPayload("evt-empty-secret", "0xabc", "Allowed"))
	forgedSig := sign(payload, "")

	if _, err := svc.Process(context.Background(), payload, forgedSig, false); err != ErrInvalidSignature {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestWebhookProcessRejectsBadSignature(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-bad-sig", "0xabc", "Allowed"))

	if _, err := svc.Process(context.Background(), payload, "deadbeef", false); err != ErrInvalidSignature {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestWebhookProcessRejectsReplay(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A2"
	if err := investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-2", addr, "Allowed"))
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, false); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	if _, err := svc.Process(context.Background(), payload, sig, false); err != ErrReplayed {
		t.Fatalf("replay err = %v, want ErrReplayed", err)
	}
}

// TestWebhookProcessRejectsReplayViaReserializedPayload checks (provider, eventId)
// uniqueness when a reserialized, re-signed delivery evades payload-hash matching
func TestWebhookProcessRejectsReplayViaReserializedPayload(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A2"
	if err := investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	svc := NewWebhookService(events, investors, testSecret)
	first := validPayload("evt-reserialize", addr, "Allowed")
	payload1, _ := json.Marshal(first)
	sig1 := sign(payload1, testSecret)
	if _, err := svc.Process(context.Background(), payload1, sig1, false); err != nil {
		t.Fatalf("first Process: %v", err)
	}

	// Add a field while preserving provider/eventId, changing both raw bytes and payload
	// hash
	raw, _ := json.Marshal(first)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	m["note"] = "resent"
	payload2, _ := json.Marshal(m)
	sig2 := sign(payload2, testSecret)

	if _, err := svc.Process(context.Background(), payload2, sig2, false); err != ErrReplayed {
		t.Fatalf("reserialized replay err = %v, want ErrReplayed", err)
	}
}

// TestWebhookProcessConcurrentIdenticalDeliveriesOnlyOneApplied checks racing identical
// payloads: exactly one Process succeeds; others return ErrReplayed even if Exists
// initially passes
func TestWebhookProcessConcurrentIdenticalDeliveriesOnlyOneApplied(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A9"
	if err := investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-concurrent", addr, "Allowed"))
	sig := sign(payload, testSecret)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := svc.Process(context.Background(), payload, sig, false)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	successes, replays := 0, 0
	for _, err := range errs {
		switch err {
		case nil:
			successes++
		case ErrReplayed:
			replays++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1", successes)
	}
	if replays != n-1 {
		t.Errorf("replays = %d, want %d", replays, n-1)
	}

	all, err := events.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("stored KYC events = %d, want exactly 1", len(all))
	}
}

func TestWebhookProcessRejectsUnverifiedOwnershipWithoutOverride(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A3" // no investor record at all
	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-3", addr, "Allowed"))
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, false); err != ErrOwnershipNotVerified {
		t.Fatalf("err = %v, want ErrOwnershipNotVerified", err)
	}
}

func TestWebhookProcessAllowsManualOverride(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A4"
	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-4", addr, "Blocked"))
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err != nil {
		t.Fatalf("Process with override: %v", err)
	}
}

func TestWebhookProcessRejectsUnknownStatus(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-unknown-status", "0xabc", "Maybe"))
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil || !strings.Contains(err.Error(), "unsupported status") {
		t.Fatalf("err = %v, want one containing %q", err, "unsupported status")
	}
}

// --- payload validation tests ---

func TestWebhookProcessRejectsMissingProvider(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-no-provider", "0x00000000000000000000000000000000000000A5", "Allowed")
	wp.Provider = ""
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil || !strings.Contains(err.Error(), "missing required provider") {
		t.Fatalf("err = %v, want one containing %q", err, "missing required provider")
	}
}

func TestWebhookProcessRejectsMissingEventID(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("", "0x00000000000000000000000000000000000000A5", "Allowed")
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil || !strings.Contains(err.Error(), "missing required eventId") {
		t.Fatalf("err = %v, want one containing %q", err, "missing required eventId")
	}
}

func TestWebhookProcessRejectsInvalidAddress(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-bad-addr", "not-an-address", "Allowed")
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil || !strings.Contains(err.Error(), "not a valid hex address") {
		t.Fatalf("err = %v, want one containing %q", err, "not a valid hex address")
	}
}

// TestWebhookProcessNormalizesAddressChecksum checks address normalization:
// a lowercase input address must come back EIP-55 checksummed
func TestWebhookProcessNormalizesAddressChecksum(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	lower := "0x00000000000000000000000000000000000000a5"
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-checksum", lower, "Allowed")
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	got, err := svc.Process(context.Background(), payload, sig, true)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if want := common.HexToAddress(lower).Hex(); got.Address != want || got.Address == lower {
		t.Errorf("Address = %s, want the EIP-55 form %s", got.Address, want)
	}
}

// TestWebhookProcessRejectsMissingOccurredAt checks that occurredAt is
// required
func TestWebhookProcessRejectsMissingOccurredAt(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-no-occurred", "0x00000000000000000000000000000000000000A6", "Allowed")
	wp.OccurredAt = 0
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil || !strings.Contains(err.Error(), "missing required occurredAt") {
		t.Fatalf("err = %v, want one containing %q", err, "missing required occurredAt")
	}
}

// TestWebhookProcessRejectsStaleOccurredAt checks that a correctly signed
// decision older than MaxWebhookAge is rejected
func TestWebhookProcessRejectsStaleOccurredAt(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-stale", "0x00000000000000000000000000000000000000A7", "Allowed")
	wp.OccurredAt = time.Now().UTC().Add(-2 * MaxWebhookAge).Unix()
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

// The delivery window is inclusive: exactly MaxWebhookAge old or exactly
// MaxWebhookClockSkewAhead ahead passes, one second beyond either is stale
func TestWebhookProcess_DeliveryWindowBoundaries(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		occurred  time.Time
		wantStale bool
	}{
		{"exactly max age", now.Add(-MaxWebhookAge), false},
		{"one second past max age", now.Add(-MaxWebhookAge - time.Second), true},
		{"exactly max skew ahead", now.Add(MaxWebhookClockSkewAhead), false},
		{"one second past max skew ahead", now.Add(MaxWebhookClockSkewAhead + time.Second), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewWebhookService(memory.NewKYCEventRepository(), memory.NewInvestorRepository(), testSecret)
			svc.now = func() time.Time { return now }
			wp := validPayload("evt-window", "0x00000000000000000000000000000000000000B1", "Allowed")
			wp.OccurredAt = tt.occurred.Unix()
			payload, _ := json.Marshal(wp)

			_, err := svc.Process(context.Background(), payload, sign(payload, testSecret), true)
			if tt.wantStale {
				if !errors.Is(err, ErrStale) {
					t.Fatalf("err = %v, want ErrStale", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want accepted", err)
			}
		})
	}
}

// validUntil may sit exactly MaxValidUntilHorizon ahead of the server clock, no further
func TestWebhookProcess_ValidUntilHorizonBoundary(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		validUntil int64
		wantErr    bool
	}{
		{"exactly at the horizon", now.Add(MaxValidUntilHorizon).Unix(), false},
		{"one second past the horizon", now.Add(MaxValidUntilHorizon).Unix() + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewWebhookService(memory.NewKYCEventRepository(), memory.NewInvestorRepository(), testSecret)
			svc.now = func() time.Time { return now }
			wp := validPayload("evt-horizon", "0x00000000000000000000000000000000000000B2", "Allowed")
			wp.OccurredAt = now.Unix()
			wp.ValidUntil = tt.validUntil
			payload, _ := json.Marshal(wp)

			_, err := svc.Process(context.Background(), payload, sign(payload, testSecret), true)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestWebhookProcessRejectsFutureOccurredAt checks that an occurredAt far in
// the future is rejected
func TestWebhookProcessRejectsFutureOccurredAt(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-future", "0x00000000000000000000000000000000000000A8", "Allowed")
	wp.OccurredAt = time.Now().UTC().Add(2 * MaxWebhookClockSkewAhead).Unix()
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

// TestWebhookProcessRejectsOutOfOrderDecision checks that an older occurredAt loses the
// address claim even with a distinct provider/eventId
func TestWebhookProcessRejectsOutOfOrderDecision(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000B1"
	svc := NewWebhookService(events, investors, testSecret)

	newer := validPayload("evt-newer", addr, "Allowed")
	newer.OccurredAt = time.Now().UTC().Unix()
	payload1, _ := json.Marshal(newer)
	if _, err := svc.Process(context.Background(), payload1, sign(payload1, testSecret), true); err != nil {
		t.Fatalf("Process newer: %v", err)
	}

	older := validPayload("evt-older", addr, "Blocked")
	older.OccurredAt = newer.OccurredAt - 3600 // an hour earlier, stale against the applied decision
	payload2, _ := json.Marshal(older)
	if _, err := svc.Process(context.Background(), payload2, sign(payload2, testSecret), true); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

// TestWebhookProcessAcceptsNewerDecisionForSameAddress checks that a newer
// decision for the same address is accepted
func TestWebhookProcessAcceptsNewerDecisionForSameAddress(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000B2"
	svc := NewWebhookService(events, investors, testSecret)

	first := validPayload("evt-first", addr, "Pending")
	first.OccurredAt = time.Now().UTC().Add(-time.Hour).Unix()
	payload1, _ := json.Marshal(first)
	if _, err := svc.Process(context.Background(), payload1, sign(payload1, testSecret), true); err != nil {
		t.Fatalf("Process first: %v", err)
	}

	second := validPayload("evt-second", addr, "Allowed")
	second.OccurredAt = time.Now().UTC().Unix()
	payload2, _ := json.Marshal(second)
	if _, err := svc.Process(context.Background(), payload2, sign(payload2, testSecret), true); err != nil {
		t.Fatalf("Process second (newer): %v", err)
	}
}

// TestWebhookProcessEqualTimestampUsesEventKeyTieBreak checks that equal-timestamp
// decisions select the lexicographically greater eventKey regardless of arrival order
func TestWebhookProcessEqualTimestampUsesEventKeyTieBreak(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000C2"
	svc := NewWebhookService(events, investors, testSecret)
	occurredAt := time.Now().UTC().Unix()

	lower := validPayload("evt-aaa", addr, "Allowed")
	lower.OccurredAt = occurredAt
	payloadLower, _ := json.Marshal(lower)
	if _, err := svc.Process(context.Background(), payloadLower, sign(payloadLower, testSecret), true); err != nil {
		t.Fatalf("Process lower-key: %v", err)
	}

	// Same occurredAt and a greater eventKey wins the claim, although it is
	// not newer in time
	higher := validPayload("evt-zzz", addr, "Blocked")
	higher.OccurredAt = occurredAt
	payloadHigher, _ := json.Marshal(higher)
	if _, err := svc.Process(context.Background(), payloadHigher, sign(payloadHigher, testSecret), true); err != nil {
		t.Fatalf("Process higher-key at equal timestamp: %v", err)
	}

	// A smaller eventKey at the claim holder's timestamp loses
	evenLower := validPayload("evt-aab", addr, "Allowed")
	evenLower.OccurredAt = occurredAt
	payloadEvenLower, _ := json.Marshal(evenLower)
	if _, err := svc.Process(context.Background(), payloadEvenLower, sign(payloadEvenLower, testSecret), true); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale (evt-aab < evt-zzz at the same timestamp)", err)
	}
}

// TestWebhookProcessConcurrentDistinctEventsAtomicClaim checks racing distinct
// decisions for one address: the greatest occurredAt must hold the claim regardless of
// scheduling
func TestWebhookProcessConcurrentDistinctEventsAtomicClaim(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000C1"
	if err := investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	svc := NewWebhookService(events, investors, testSecret)

	const n = 20
	base := time.Now().UTC().Add(-time.Hour).Unix() // keep every occurredAt comfortably inside the window
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			wp := validPayload(fmt.Sprintf("evt-distinct-%d", i), addr, "Allowed")
			wp.OccurredAt = base + int64(i) // strictly increasing -> a unique global maximum at i == n-1
			payload, _ := json.Marshal(wp)
			_, err := svc.Process(context.Background(), payload, sign(payload, testSecret), false)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	if errs[n-1] != nil {
		t.Fatalf("the strictly-newest event must always win its claim, got %v", errs[n-1])
	}
	successes := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrStale):
			// lost the claim to a concurrently-processed, newer event, expected
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	// Store all n deliveries before resolving claims; winners are Accepted and losers
	// Superseded
	all, err := events.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Errorf("stored KYC events = %d, want %d (one per distinct delivery)", len(all), n)
	}
}

// TestWebhookProcessRejectsNegativeValidUntil checks that a negative validUntil
// is rejected before the unsigned conversion wraps it into a huge expiry
func TestWebhookProcessRejectsNegativeValidUntil(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-negative-validuntil", "0x00000000000000000000000000000000000000B3", "Allowed")
	wp.ValidUntil = -1
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for negative validUntil")
	}
}

// TestWebhookProcessRejectsValidUntilBeyondPolicyHorizon checks that a
// validUntil beyond MaxValidUntilHorizon is rejected
func TestWebhookProcessRejectsValidUntilBeyondPolicyHorizon(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-validuntil-horizon", "0x00000000000000000000000000000000000000B4", "Allowed")
	wp.ValidUntil = time.Now().UTC().Add(2 * MaxValidUntilHorizon).Unix()
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for validUntil beyond the policy horizon")
	}
}

// --- partial-failure recovery tests ---

var errInjected = errors.New("injected persistence failure")

// faultyKYCRepo injects failures after Create, during claiming or finalization. Both
// must retain a Claiming event for reconciler recovery.
type faultyKYCRepo struct {
	*memory.KYCEventRepository
	failClaim  bool
	failUpdate int // fail this many subsequent Update calls, then stop
}

func (f *faultyKYCRepo) ClaimLatestForAddress(ctx context.Context, address string, occurredAt time.Time, eventKey string) (bool, error) {
	if f.failClaim {
		return false, errInjected
	}
	return f.KYCEventRepository.ClaimLatestForAddress(ctx, address, occurredAt, eventKey)
}

func (f *faultyKYCRepo) Update(ctx context.Context, e *models.KYCEvent) error {
	if f.failUpdate > 0 {
		f.failUpdate--
		return errInjected
	}
	return f.KYCEventRepository.Update(ctx, e)
}

// TestWebhookProcessRecoversWhenClaimFailsAfterCreate checks that a failed claim leaves
// Claiming for reconciliation to Accepted
func TestWebhookProcessRecoversWhenClaimFailsAfterCreate(t *testing.T) {
	base := memory.NewKYCEventRepository()
	repo := &faultyKYCRepo{KYCEventRepository: base, failClaim: true}
	investors := memory.NewInvestorRepository()
	addr := "0x0000000000000000000000000000000000000E01"
	svc := NewWebhookService(repo, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-e01", addr, "Blocked"))

	if _, err := svc.Process(context.Background(), payload, sign(payload, testSecret), true); err == nil {
		t.Fatal("expected the injected claim failure to surface")
	}
	all, err := base.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ApplyStatus != models.KYCApplyClaiming {
		t.Fatalf("want exactly one durable Claiming event, got %+v", all)
	}

	repo.failClaim = false
	rec := NewWebhookReconciler(repo, memory.NewTransactionRepository(), nil)
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := base.List(context.Background())
	if got[0].ApplyStatus != models.KYCApplyAccepted {
		t.Fatalf("ApplyStatus = %s, want Accepted after recovery", got[0].ApplyStatus)
	}
	key, err := base.CurrentClaimEventKey(context.Background(), got[0].Address)
	if err != nil || key != "test-provider|evt-e01" {
		t.Fatalf("claim = %q err %v, want the recovered event to hold it", key, err)
	}
}

// TestWebhookProcessRecoversWhenFinalizeUpdateFails checks that a successful claim with
// failed finalization stays Claiming until reconciliation restores Accepted
func TestWebhookProcessRecoversWhenFinalizeUpdateFails(t *testing.T) {
	base := memory.NewKYCEventRepository()
	repo := &faultyKYCRepo{KYCEventRepository: base, failUpdate: 1}
	investors := memory.NewInvestorRepository()
	addr := "0x0000000000000000000000000000000000000E02"
	svc := NewWebhookService(repo, investors, testSecret)
	payload, _ := json.Marshal(validPayload("evt-e02", addr, "Blocked"))

	if _, err := svc.Process(context.Background(), payload, sign(payload, testSecret), true); err == nil {
		t.Fatal("expected the injected update failure to surface")
	}
	all, _ := base.List(context.Background())
	if len(all) != 1 || all[0].ApplyStatus != models.KYCApplyClaiming {
		t.Fatalf("want a durable Claiming event, got %+v", all)
	}

	rec := NewWebhookReconciler(repo, memory.NewTransactionRepository(), nil)
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := base.List(context.Background())
	if got[0].ApplyStatus != models.KYCApplyAccepted {
		t.Fatalf("ApplyStatus = %s, want Accepted after recovery", got[0].ApplyStatus)
	}
}

// TestWebhookProcessNewestBlockedRecoverableAfterClaimFailure checks that a newer
// Blocked decision recovers its failed claim, supersedes older Allowed and queues one
// application
func TestWebhookProcessNewestBlockedRecoverableAfterClaimFailure(t *testing.T) {
	base := memory.NewKYCEventRepository()
	repo := &faultyKYCRepo{KYCEventRepository: base}
	investors := memory.NewInvestorRepository()
	addr := "0x0000000000000000000000000000000000000E03"
	svc := NewWebhookService(repo, investors, testSecret)
	t0 := time.Now().UTC().Add(-time.Hour)

	allowed := validPayload("evt-allowed", addr, "Allowed")
	allowed.OccurredAt = t0.Unix()
	p1, _ := json.Marshal(allowed)
	if _, err := svc.Process(context.Background(), p1, sign(p1, testSecret), true); err != nil {
		t.Fatalf("Process allowed: %v", err)
	}

	repo.failClaim = true
	blocked := validPayload("evt-blocked", addr, "Blocked")
	blocked.OccurredAt = t0.Add(time.Minute).Unix()
	p2, _ := json.Marshal(blocked)
	if _, err := svc.Process(context.Background(), p2, sign(p2, testSecret), true); err == nil {
		t.Fatal("expected the injected claim failure to surface for the Blocked decision")
	}
	repo.failClaim = false

	// First finalize the Blocked claim, then supersede stale Allowed on the next tick
	rec := NewWebhookReconciler(repo, memory.NewTransactionRepository(), nil)
	for i := 0; i < 2; i++ {
		if err := rec.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile #%d: %v", i, err)
		}
	}

	all, _ := base.List(context.Background())
	key, err := base.CurrentClaimEventKey(context.Background(), all[0].Address)
	if err != nil || key != "test-provider|evt-blocked" {
		t.Fatalf("claim = %q err %v, want the Blocked decision to hold it", key, err)
	}
	var blockedEvt, allowedEvt *models.KYCEvent
	for _, e := range all {
		switch e.EventID {
		case "evt-blocked":
			blockedEvt = e
		case "evt-allowed":
			allowedEvt = e
		}
	}
	if blockedEvt == nil || blockedEvt.ApplyStatus != models.KYCApplyAccepted {
		t.Fatalf("blocked event = %+v, want Accepted (queued to apply)", blockedEvt)
	}
	if allowedEvt == nil || allowedEvt.ApplyStatus != models.KYCApplySuperseded {
		t.Fatalf("allowed event = %+v, want Superseded", allowedEvt)
	}
}
