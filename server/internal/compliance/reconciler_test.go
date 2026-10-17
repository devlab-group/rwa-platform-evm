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

// reconcilerFixture uses memory KYC/transaction repositories and FakeClient to drive
// Pending-to-Confirmed submission as the background loop does
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

// seedAccepted stores and claims the state ProcessDecision leaves for relay, bypassing
// webhook handling
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

// TestWebhookReconcilerSubmitsThenConfirms covers the normal path: Accepted,
// then Applying once a transaction is submitted, then Applied only after the
// transaction confirms
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
	if acct, status := submittedStatus(t, tx); acct != common.HexToAddress(addr) || status != 1 {
		t.Fatalf("relayed setStatus(%s, %d), want (%s, 1 = Allowed)", acct, status, addr)
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

// TestWebhookReconcilerRetriesAfterTxFailed checks that a rejected send returns the
// event to Accepted for same-key resubmission next tick
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

	// The next tick resubmits under the same idempotency key, so TxManager
	// reuses the same transaction record id
	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (resubmit): %v", err)
	}
	got = f.get(t, "evt-d2")
	if got.ApplyStatus != models.KYCApplyApplying || got.TxID != firstTxID {
		t.Fatalf("got %+v, want Applying reusing tx id %s", got, firstTxID)
	}
}

// TestWebhookReconcilerMarksRevertedTxFailed checks that a reverted
// transaction ends the event as Failed and is not retried
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
	if acct, status := submittedStatus(t, tx); acct != common.HexToAddress(addr) || status != 2 {
		t.Fatalf("relayed setStatus(%s, %d), want (%s, 2 = Blocked)", acct, status, addr)
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

// TestWebhookReconcilerSupersedesStaleAcceptedEvent checks that an Accepted
// event whose address has since been claimed by a newer decision is marked
// Superseded and never submitted
func TestWebhookReconcilerSupersedesStaleAcceptedEvent(t *testing.T) {
	f := newReconcilerFixture(t)
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000D4"
	t0 := time.Now().UTC().Add(-time.Hour)
	f.seedAccepted(t, "evt-d4-old", addr, "test-provider", "evt-d4-old", "Blocked", t0)
	// A newer delivery claims the address while the older Accepted event is queued
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

// TestWebhookReconcilerRetriesAfterTxReorged checks that an orphaned transaction clears
// TxID, restores Accepted and resubmits under the same key next tick
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

// TestWebhookReconcilerReopensApplyStatusAfterPostConfirmationReorg checks that a
// reorged Applied event returns to Accepted for resubmission
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

	// A reorg after the event reached Applied drops the confirming block
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

	// A later tick resubmits it like any other Accepted event
	if err := f.r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (resubmit): %v", err)
	}
	got = f.get(t, "evt-d7")
	if got.ApplyStatus != models.KYCApplyApplying || got.TxID != txID {
		t.Fatalf("got %+v, want Applying reusing tx id %s", got, txID)
	}
}

// TestWebhookReconcilerLeavesAcceptedEventWithoutStatusService checks that
// without a compliance signer the event stays Accepted and is never reported
// Applied
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

// submittedStatus reads the (account, status) arguments of a setStatus transaction
func submittedStatus(t *testing.T, tx *models.Transaction) (common.Address, int64) {
	t.Helper()
	raw := common.FromHex(tx.Data)
	if len(raw) != 4+3*32 {
		t.Fatalf("calldata is %d bytes, want a setStatus call of 100", len(raw))
	}
	return common.BytesToAddress(raw[4:36]), new(big.Int).SetBytes(raw[36:68]).Int64()
}
