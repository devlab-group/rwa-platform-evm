package compliance

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

// reconcilerFixture wires a WebhookReconciler against real (in-memory)
// KYCEventRepository/TransactionRepository implementations and a
// FakeClient, so tests can drive a submission all the way through
// Pending -> Confirmed the same way cmd/platform's background loop does.
type reconcilerFixture struct {
	r      *WebhookReconciler
	events *memory.KYCEventRepository
	txRepo *memory.TransactionRepository
	client *blockchain.FakeClient
	txs    blockchain.TxManager
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	events := memory.NewKYCEventRepository()
	txRepo := memory.NewTransactionRepository()
	client := blockchain.NewFakeClient()
	txs := blockchain.NewTxManager(client, txRepo, big.NewInt(31337), blockchain.FeeModeLegacy)
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	status := NewStatusService(txs, common.HexToAddress("0x00000000000000000000000000000000C0A1"), blockchain.NewStaticKeySigner(priv))
	return &reconcilerFixture{
		r:      NewWebhookReconciler(events, txRepo, status),
		events: events, txRepo: txRepo, client: client, txs: txs,
	}
}

// seedAccepted claims and durably stores an Accepted event, exactly the
// state WebhookService.Process leaves one in for the reconciler to pick
// up — used so these tests exercise the reconciler in isolation without
// going through the full HMAC/Process path.
func (f *reconcilerFixture) seedAccepted(t *testing.T, id, address, provider, eventID, outcome string, occurredAt time.Time) *models.KYCEvent {
	t.Helper()
	ctx := context.Background()
	won, err := f.events.ClaimLatestForAddress(ctx, address, occurredAt, kycEventIDKey(provider, eventID))
	if err != nil {
		t.Fatal(err)
	}
	if !won {
		t.Fatalf("seedAccepted: claim for %s/%s did not win", provider, eventID)
	}
	e := &models.KYCEvent{
		ID: id, Address: address, Provider: provider, EventID: eventID, Status: outcome, Outcome: outcome,
		ApplyStatus: models.KYCApplyAccepted, OccurredAt: occurredAt, ReceivedAt: occurredAt, PayloadHash: id,
	}
	if err := f.events.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *reconcilerFixture) get(t *testing.T, id string) *models.KYCEvent {
	t.Helper()
	all, err := f.events.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("event %s not found", id)
	return nil
}

// TestWebhookReconcilerSubmitsThenConfirms is the happy path:
// Accepted -> Applying (a tx intent is submitted) -> Applied, ONLY once
// the linked transaction actually confirms on-chain.
func TestWebhookReconcilerSubmitsThenConfirms(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D1"
	f.seedAccepted(t, "evt-d1", addr, "test-provider", "evt-d1", "Allowed", time.Now().UTC())

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (submit): %v", err)
	}
	got := f.get(t, "evt-d1")
	if got.ApplyStatus != models.KYCApplyApplying || got.TxID == "" {
		t.Fatalf("got %+v, want Applying with a TxID", got)
	}

	tx, err := f.txRepo.Get(ctx, got.TxID)
	if err != nil {
		t.Fatal(err)
	}
	f.client.SetReceipt(common.HexToHash(tx.TxHash), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(1)})
	if err := f.txs.RefreshStatuses(ctx, 0); err != nil {
		t.Fatal(err)
	}

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (confirm): %v", err)
	}
	got = f.get(t, "evt-d1")
	if got.ApplyStatus != models.KYCApplyApplied {
		t.Fatalf("ApplyStatus = %s, want Applied", got.ApplyStatus)
	}
}

// TestWebhookReconcilerRetriesAfterTxFailed covers idempotency-key reuse on
// retry: a broadcast that never reached the mempool (TxFailed) must
// go back to Accepted and resubmit under the SAME idempotency key next
// tick, not get stuck or double-submit under a new one.
func TestWebhookReconcilerRetriesAfterTxFailed(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D2"
	f.seedAccepted(t, "evt-d2", addr, "test-provider", "evt-d2", "Allowed", time.Now().UTC())

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (submit): %v", err)
	}
	got := f.get(t, "evt-d2")
	firstTxID := got.TxID

	tx, err := f.txRepo.Get(ctx, firstTxID)
	if err != nil {
		t.Fatal(err)
	}
	tx.Status = models.TxFailed
	if err := f.txRepo.Update(ctx, tx); err != nil {
		t.Fatal(err)
	}

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (detect failed): %v", err)
	}
	got = f.get(t, "evt-d2")
	if got.ApplyStatus != models.KYCApplyAccepted || got.TxID != "" {
		t.Fatalf("got %+v, want Accepted with TxID cleared after a TxFailed broadcast", got)
	}

	// Next tick resubmits — under the identical idempotency key, so
	// TxManager reuses the SAME transaction record id (a TxFailed record's
	// Idempotency-Key is retryable).
	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (resubmit): %v", err)
	}
	got = f.get(t, "evt-d2")
	if got.ApplyStatus != models.KYCApplyApplying || got.TxID != firstTxID {
		t.Fatalf("got %+v, want Applying reusing tx id %s", got, firstTxID)
	}
}

// TestWebhookReconcilerMarksRevertedTxFailed covers a genuine on-chain
// rejection: it's terminal (Failed), not silently retried forever like a
// TxFailed broadcast ambiguity is.
func TestWebhookReconcilerMarksRevertedTxFailed(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D3"
	f.seedAccepted(t, "evt-d3", addr, "test-provider", "evt-d3", "Blocked", time.Now().UTC())

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, "evt-d3")
	tx, err := f.txRepo.Get(ctx, got.TxID)
	if err != nil {
		t.Fatal(err)
	}
	f.client.SetReceipt(common.HexToHash(tx.TxHash), &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: big.NewInt(1)})
	if err := f.txs.RefreshStatuses(ctx, 0); err != nil {
		t.Fatal(err)
	}

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got = f.get(t, "evt-d3")
	if got.ApplyStatus != models.KYCApplyFailed {
		t.Fatalf("ApplyStatus = %s, want Failed", got.ApplyStatus)
	}
}

// TestWebhookReconcilerSupersedesStaleAcceptedEvent covers the "never apply a
// stale decision" case: an Accepted event that a NEWER decision
// for the same address has since claimed must never be submitted at all —
// it's marked Superseded instead.
func TestWebhookReconcilerSupersedesStaleAcceptedEvent(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D4"
	t0 := time.Now().UTC().Add(-time.Hour)
	f.seedAccepted(t, "evt-d4-old", addr, "test-provider", "evt-d4-old", "Blocked", t0)
	// A newer decision for the SAME address claims it before the old one
	// is ever reconciled — simulating a second webhook delivery arriving
	// while the first is still queued.
	f.seedAccepted(t, "evt-d4-new", addr, "test-provider", "evt-d4-new", "Allowed", t0.Add(time.Minute))

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	old := f.get(t, "evt-d4-old")
	if old.ApplyStatus != models.KYCApplySuperseded {
		t.Fatalf("old event ApplyStatus = %s, want Superseded", old.ApplyStatus)
	}
	if old.TxID != "" {
		t.Fatalf("superseded event must never have been submitted, got TxID %q", old.TxID)
	}
	newer := f.get(t, "evt-d4-new")
	if newer.ApplyStatus != models.KYCApplyApplying {
		t.Fatalf("newer event ApplyStatus = %s, want Applying (it should have been the one submitted)", newer.ApplyStatus)
	}
}

// TestWebhookReconcilerRetriesAfterTxReorged is the reorg counterpart to
// TestWebhookReconcilerRetriesAfterTxFailed: a linked transaction that was
// mined but later reorged out (TxReorged) must behave the same way as a
// TxFailed one from the reconciler's perspective — go back to Accepted with
// TxID cleared, then resubmit under the identical idempotency key next
// tick.
func TestWebhookReconcilerRetriesAfterTxReorged(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D6"
	f.seedAccepted(t, "evt-d6", addr, "test-provider", "evt-d6", "Allowed", time.Now().UTC())

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (submit): %v", err)
	}
	got := f.get(t, "evt-d6")
	firstTxID := got.TxID

	tx, err := f.txRepo.Get(ctx, firstTxID)
	if err != nil {
		t.Fatal(err)
	}
	tx.Status = models.TxReorged
	if err := f.txRepo.Update(ctx, tx); err != nil {
		t.Fatal(err)
	}

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (detect reorged): %v", err)
	}
	got = f.get(t, "evt-d6")
	if got.ApplyStatus != models.KYCApplyAccepted || got.TxID != "" {
		t.Fatalf("got %+v, want Accepted with TxID cleared after a TxReorged linked transaction", got)
	}

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (resubmit): %v", err)
	}
	got = f.get(t, "evt-d6")
	if got.ApplyStatus != models.KYCApplyApplying || got.TxID != firstTxID {
		t.Fatalf("got %+v, want Applying reusing tx id %s", got, firstTxID)
	}
}

// TestWebhookReconcilerReopensApplyStatusAfterPostConfirmationReorg covers the
// post-confirmation reorg case: an event that already reached the TERMINAL
// KYCApplyApplied state (its linked transaction had confirmed) must not
// stay permanently "Applied" once that same transaction later reorgs out —
// Reconcile must reopen it back to Accepted so it gets re-submitted, rather
// than silently leaving a KYC decision recorded as applied on-chain when it
// no longer is.
func TestWebhookReconcilerReopensApplyStatusAfterPostConfirmationReorg(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D7"
	f.seedAccepted(t, "evt-d7", addr, "test-provider", "evt-d7", "Allowed", time.Now().UTC())

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (submit): %v", err)
	}
	got := f.get(t, "evt-d7")
	txID := got.TxID

	tx, err := f.txRepo.Get(ctx, txID)
	if err != nil {
		t.Fatal(err)
	}
	f.client.SetReceipt(common.HexToHash(tx.TxHash), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(1)})
	if err := f.txs.RefreshStatuses(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (confirm): %v", err)
	}
	got = f.get(t, "evt-d7")
	if got.ApplyStatus != models.KYCApplyApplied {
		t.Fatalf("precondition: ApplyStatus = %s, want Applied", got.ApplyStatus)
	}

	// A deep reorg, well after the event already reached the terminal
	// Applied state, drops the confirming block.
	tx, err = f.txRepo.Get(ctx, txID)
	if err != nil {
		t.Fatal(err)
	}
	tx.Status = models.TxReorged
	if err := f.txRepo.Update(ctx, tx); err != nil {
		t.Fatal(err)
	}

	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (reopen): %v", err)
	}
	got = f.get(t, "evt-d7")
	if got.ApplyStatus != models.KYCApplyAccepted || got.TxID != "" {
		t.Fatalf("got %+v, want reopened to Accepted with TxID cleared after the linked tx reorged", got)
	}

	// And it gets properly re-driven on a later tick, same as any other
	// Accepted event.
	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (resubmit): %v", err)
	}
	got = f.get(t, "evt-d7")
	if got.ApplyStatus != models.KYCApplyApplying || got.TxID != txID {
		t.Fatalf("got %+v, want Applying reusing tx id %s", got, txID)
	}
}

// TestWebhookReconcilerLeavesAcceptedEventWithoutStatusService documents
// the deliberate "stuck, never fabricated Applied" behavior when no
// compliance signer is configured — see submit's doc comment.
func TestWebhookReconcilerLeavesAcceptedEventWithoutStatusService(t *testing.T) {
	events := memory.NewKYCEventRepository()
	txRepo := memory.NewTransactionRepository()
	r := NewWebhookReconciler(events, txRepo, nil)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D5"
	occurredAt := time.Now().UTC()
	if _, err := events.ClaimLatestForAddress(ctx, addr, occurredAt, kycEventIDKey("test-provider", "evt-d5")); err != nil {
		t.Fatal(err)
	}
	if err := events.Create(ctx, &models.KYCEvent{
		ID: "evt-d5", Address: addr, Provider: "test-provider", EventID: "evt-d5", Outcome: "Allowed",
		ApplyStatus: models.KYCApplyAccepted, OccurredAt: occurredAt, ReceivedAt: occurredAt, PayloadHash: "evt-d5",
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	all, err := events.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("events = %v, err %v", all, err)
	}
	if all[0].ApplyStatus != models.KYCApplyAccepted {
		t.Fatalf("ApplyStatus = %s, want it to stay Accepted (never fabricate Applied without a signer)", all[0].ApplyStatus)
	}
}
