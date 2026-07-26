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

// validPayload returns a WebhookPayload satisfying every required field
// (provider, eventId, occurredAt) with a fresh occurredAt, so individual
// tests only need to override the one field they care about.
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

// TestVerifyHMACRejectsEmptySecret checks that an empty secret is refused:
// HMAC-SHA256 with an empty key is a well-defined, publicly computable
// function, so an
// attacker who knows the secret is unconfigured could otherwise compute a
// "valid" signature for any payload. VerifyHMAC must refuse an empty secret
// outright rather than deferring to the underlying HMAC comparison.
func TestVerifyHMACRejectsEmptySecret(t *testing.T) {
	payload := []byte(`{"address":"0xattacker","status":"Allowed"}`)
	forgedSig := sign(payload, "") // exactly what an attacker who knows the secret is empty would compute
	if VerifyHMAC(payload, forgedSig, "") {
		t.Fatal("VerifyHMAC accepted a signature computed with an empty secret")
	}
}

// TestWebhookProcessRejectsEmptySecret is the same check at the
// Process layer: a WebhookService constructed with an empty secret (which
// cmd/platform's buildApp avoids doing at all) must still
// fail closed as defense-in-depth.
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

// TestWebhookProcessRejectsReplayViaReserializedPayload covers the case where
// the same logical event is reserialized and re-signed as a new payload:
// dedup by raw payload hash alone is
// insufficient — the (provider,eventId) uniqueness constraint must catch a
// byte-different resend of the same logical decision.
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

	// Same logical event (same provider+eventId), reserialized with a
	// trailing field so the raw bytes — and therefore the payload hash —
	// differ from the first delivery.
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

// TestWebhookProcessConcurrentIdenticalDeliveriesOnlyOneApplied is the S4
// regression test: webhook providers commonly retry deliveries, sometimes
// concurrently (e.g. after an ambiguous timeout). N goroutines Process the
// exact same signed payload at once; exactly one may succeed, the rest
// must get ErrReplayed — never each independently pass the (racy)
// Exists() pre-check and both Create() a KYCEvent for the same decision.
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

// --- payload validation regression tests ---

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
// a lowercase input address must come back EIP-55 checksummed.
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

// TestWebhookProcessRejectsMissingOccurredAt: a signed occurrence timestamp
// is required.
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

// TestWebhookProcessRejectsStaleOccurredAt enforces a bounded delivery
// window: an old, correctly signed decision that was never delivered must
// not remain valid indefinitely.
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

// TestWebhookProcessRejectsFutureOccurredAt covers the other end of the
// bounded window: an implausibly-far-future timestamp is rejected, not
// silently accepted as "fresh."
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

// TestWebhookProcessRejectsOutOfOrderDecision covers ordering of
// newer/older decisions: a second, DIFFERENT
// eventId for the same address whose occurredAt is not strictly newer than
// the already-applied decision must be rejected, even though it isn't a
// literal (provider,eventId) replay.
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
	older.OccurredAt = newer.OccurredAt - 3600 // an hour earlier — stale relative to what's already applied
	payload2, _ := json.Marshal(older)
	if _, err := svc.Process(context.Background(), payload2, sign(payload2, testSecret), true); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
}

// TestWebhookProcessAcceptsNewerDecisionForSameAddress is the positive
// counterpart: a genuinely newer decision for the same address must be
// accepted, not blocked by the ordering check.
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
// deterministic (occurredAt,eventKey) tie-break:
// two DIFFERENT events for the same address at the EXACT same occurredAt
// must resolve identically everywhere — the lexicographically greater
// eventKey wins, regardless of arrival order.
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

	// Same occurredAt, lexicographically GREATER eventKey — must still win
	// the claim under the tie-break even though it isn't strictly newer
	// in time.
	higher := validPayload("evt-zzz", addr, "Blocked")
	higher.OccurredAt = occurredAt
	payloadHigher, _ := json.Marshal(higher)
	if _, err := svc.Process(context.Background(), payloadHigher, sign(payloadHigher, testSecret), true); err != nil {
		t.Fatalf("Process higher-key at equal timestamp: %v", err)
	}

	// And the reverse: a lexicographically SMALLER eventKey at the same
	// timestamp as the current claim holder must lose.
	evenLower := validPayload("evt-aab", addr, "Allowed")
	evenLower.OccurredAt = occurredAt
	payloadEvenLower, _ := json.Marshal(evenLower)
	if _, err := svc.Process(context.Background(), payloadEvenLower, sign(payloadEvenLower, testSecret), true); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale (evt-aab < evt-zzz at the same timestamp)", err)
	}
}

// TestWebhookProcessConcurrentDistinctEventsAtomicClaim is the counterpart
// to TestWebhookProcessConcurrentIdenticalDeliveriesOnlyOneApplied:
// N goroutines Process DIFFERENT events (distinct eventIds, distinct
// strictly-increasing occurredAt) for the SAME address concurrently. The
// globally newest event can never lose its claim regardless of scheduling
// — nothing concurrent can ever be newer than it — which is a scheduling-
// order-independent way to prove ClaimLatestForAddress's compare-and-set
// is genuinely atomic rather than racy.
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
			// lost the claim to a concurrently-processed, newer event — expected
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	// Every distinct delivery is durably recorded in the
	// append-only history BEFORE the claim is resolved (winners as
	// Accepted, losers as Superseded), so all n are stored — unlike the
	// old claim-first order, which only persisted claim winners.
	all, err := events.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Errorf("stored KYC events = %d, want %d (one per distinct delivery)", len(all), n)
	}
}

// TestWebhookProcessRejectsNegativeValidUntil guards the int64->uint64 cast:
// a negative validUntil must be rejected
// BEFORE any unsigned conversion, not silently wrapped into a huge expiry.
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

// TestWebhookProcessRejectsValidUntilBeyondPolicyHorizon rejects
// out-of-policy expirations.
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

// faultyKYCRepo wraps the in-memory KYCEventRepository and injects failures
// at specific persistence boundaries, so these tests can reproduce a
// crash/transient error between the durable event write and the claim
// advancement and prove the decision is never lost. Because Process now
// Creates the event BEFORE advancing the claim, the old "claim succeeds /
// event insert fails" loss window is structurally impossible; the boundaries
// that remain are "event insert succeeds / claim (or finalize) fails", both
// of which leave a durable Claiming event the reconciler recovers.
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

// TestWebhookProcessRecoversWhenClaimFailsAfterCreate reproduces the exact
// loss scenario — a transient failure right where the claim would have
// advanced — and asserts the decision survives as a durable Claiming event
// that the reconciler finalizes to Accepted.
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

// TestWebhookProcessRecoversWhenFinalizeUpdateFails covers the other
// boundary: the claim CAS succeeds but the state-write that promotes the
// event out of Claiming fails. The event stays durably Claiming and the
// reconciler still finalizes it to Accepted.
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

// TestWebhookProcessNewestBlockedRecoverableAfterClaimFailure is the core
// recovery invariant: with an older Allowed decision already Accepted, a newer
// Blocked decision whose claim advance fails must still be recovered — it
// wins the claim, the older Allowed is superseded, and the Blocked decision
// is queued to apply exactly once and is never lost.
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

	// Two ticks reach steady state: the first finalizes the Blocked claim and
	// advances the pointer; the second supersedes the now-stale Allowed event.
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
