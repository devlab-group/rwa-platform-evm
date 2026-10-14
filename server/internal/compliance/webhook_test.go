package compliance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

const testSecret = "webhook-secret"

func sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// validPayload returns a WebhookPayload with every required field set and a
// fresh occurredAt, so a test overrides only the field it cares about
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
	_ = investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true, UpdatedAt: time.Now()})

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

// TestVerifyHMACRejectsEmptySecret checks that VerifyHMAC refuses an empty
// secret. HMAC-SHA256 with an empty key is computable by anyone, so otherwise
// an attacker could sign any payload for an unconfigured webhook.
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
	_ = investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true})

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

// TestWebhookProcessRejectsReplayViaReserializedPayload covers the same event
// reserialized and re-signed as a new payload. The payload hash does not match,
// so the (provider, eventId) uniqueness constraint must catch the resend.
func TestWebhookProcessRejectsReplayViaReserializedPayload(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A2"
	_ = investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true})

	svc := NewWebhookService(events, investors, testSecret)
	first := validPayload("evt-reserialize", addr, "Allowed")
	payload1, _ := json.Marshal(first)
	sig1 := sign(payload1, testSecret)
	if _, err := svc.Process(context.Background(), payload1, sig1, false); err != nil {
		t.Fatalf("first Process: %v", err)
	}

	// The same provider and eventId with a trailing field added, so the raw
	// bytes and payload hash differ from the first delivery
	raw, _ := json.Marshal(first)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["note"] = "resent"
	payload2, _ := json.Marshal(m)
	sig2 := sign(payload2, testSecret)

	if _, err := svc.Process(context.Background(), payload2, sig2, false); err != ErrReplayed {
		t.Fatalf("reserialized replay err = %v, want ErrReplayed", err)
	}
}

// TestWebhookProcessConcurrentIdenticalDeliveriesOnlyOneApplied has N
// goroutines Process the same signed payload at once, as when a provider
// retries after a timeout. Exactly one may succeed and the rest must get
// ErrReplayed, even though several can pass the Exists pre-check.
func TestWebhookProcessConcurrentIdenticalDeliveriesOnlyOneApplied(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000A9"
	_ = investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true})

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

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for unsupported status")
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

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestWebhookProcessRejectsMissingEventID(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("", "0x00000000000000000000000000000000000000A5", "Allowed")
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for missing eventId")
	}
}

func TestWebhookProcessRejectsInvalidAddress(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	svc := NewWebhookService(events, investors, testSecret)
	wp := validPayload("evt-bad-addr", "not-an-address", "Allowed")
	payload, _ := json.Marshal(wp)
	sig := sign(payload, testSecret)

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for an invalid address")
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
	if got.Address == lower {
		t.Errorf("expected address to be normalized/checksummed, still lowercase: %s", got.Address)
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

	if _, err := svc.Process(context.Background(), payload, sig, true); err == nil {
		t.Fatal("expected error for missing occurredAt")
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

// TestWebhookProcessRejectsOutOfOrderDecision checks that a second event for
// the same address whose occurredAt is older than the claim holder's is
// rejected, although it is not a (provider, eventId) replay
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

// TestWebhookProcessEqualTimestampUsesEventKeyTieBreak covers the
// (occurredAt, eventKey) tie-break. Of two events for one address with the same
// occurredAt, the lexicographically greater eventKey wins regardless of arrival
// order.
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

// TestWebhookProcessConcurrentDistinctEventsAtomicClaim has N goroutines
// Process different events (distinct eventIds, increasing occurredAt) for one
// address. The newest event can never lose the claim whatever the scheduling,
// which shows that ClaimLatestForAddress is atomic.
func TestWebhookProcessConcurrentDistinctEventsAtomicClaim(t *testing.T) {
	events := memory.NewKYCEventRepository()
	investors := memory.NewInvestorRepository()
	addr := "0x00000000000000000000000000000000000000C1"
	_ = investors.Upsert(context.Background(), &models.Investor{Address: addr, OwnershipVerified: true})
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
	// Every delivery is stored before its claim is resolved, winners as
	// Accepted and losers as Superseded, so all n are stored
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

// faultyKYCRepo wraps the in-memory KYCEventRepository and injects failures at
// the persistence boundaries after the event is created. The event is created
// before the claim advances, so the failures to cover are the claim failing and
// the finalize update failing, and both leave a Claiming event that the
// reconciler recovers.
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

// TestWebhookProcessRecoversWhenClaimFailsAfterCreate checks that when the
// claim fails after Create, the decision stays as a Claiming event and the
// reconciler finalizes it to Accepted
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

// TestWebhookProcessRecoversWhenFinalizeUpdateFails covers the claim
// succeeding while the update that moves the event out of Claiming fails. The
// event stays Claiming and the reconciler finalizes it to Accepted.
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

// TestWebhookProcessNewestBlockedRecoverableAfterClaimFailure checks that with
// an older Allowed decision already Accepted, a newer Blocked decision whose
// claim fails is recovered. It wins the claim, the Allowed event is superseded,
// and the Blocked decision is queued to apply once.
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

	// Two ticks settle it: the first finalizes the Blocked claim, the second
	// supersedes the stale Allowed event
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
