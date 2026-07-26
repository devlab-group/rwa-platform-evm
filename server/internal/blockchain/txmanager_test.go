package blockchain

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- Broadcast/receipt lifecycle regression tests ---

// TestTxManagerSubmitAmbiguousBroadcastMarksUnknown covers the "broadcast
// timeout after node acceptance" case: a transport-level
// SendTransaction error (here, context.DeadlineExceeded — indistinguishable
// from "the node accepted it and the ack was lost") must NOT be treated as
// a definite failure. The record must persist as TxBroadcastUnknown (not
// TxFailed) with the exact signed bytes, and a same-key retry must return
// that record as-is rather than signing and broadcasting a second time.
func TestTxManagerSubmitAmbiguousBroadcastMarksUnknown(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.SendTransactionErr = context.DeadlineExceeded
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	req := SubmitRequest{IdempotencyKey: "key-ambiguous", PrivateKey: priv, To: to}

	if _, err := mgr.Submit(context.Background(), req); err == nil {
		t.Fatal("expected Submit to return an error for an ambiguous broadcast outcome")
	}
	if len(client.SentTxs) != 0 {
		t.Fatalf("FakeClient.SendTransaction errored, so nothing should be recorded sent, got %d", len(client.SentTxs))
	}

	stored, err := txRepo.GetByIdempotencyKey(context.Background(), "key-ambiguous")
	if err != nil {
		t.Fatalf("expected a persisted record even though broadcast outcome is unknown: %v", err)
	}
	if stored.Status != models.TxBroadcastUnknown {
		t.Fatalf("Status = %s, want %s", stored.Status, models.TxBroadcastUnknown)
	}
	if stored.SignedRawTx == "" {
		t.Fatal("expected the exact signed RLP bytes to be persisted before broadcast")
	}
	origHash, origNonce := stored.TxHash, stored.Nonce

	// A same-key retry must NOT sign/broadcast a second transaction while
	// the outcome is unknown — unlike TxFailed/TxReorged, TxBroadcastUnknown
	// is not retryable via Submit; it just returns the existing record.
	if _, err := mgr.Submit(context.Background(), req); err != nil {
		t.Fatalf("expected a same-key retry to return the existing ambiguous record without error, got %v", err)
	}
	if len(client.SentTxs) != 0 {
		t.Fatalf("a same-key retry must not broadcast a second time while ambiguous, got %d sent", len(client.SentTxs))
	}
	stored2, err := txRepo.GetByIdempotencyKey(context.Background(), "key-ambiguous")
	if err != nil {
		t.Fatal(err)
	}
	if stored2.ID != stored.ID || stored2.TxHash != origHash || stored2.Nonce != origNonce {
		t.Fatalf("same-key retry must not allocate a new nonce/signature, got %+v vs %+v", stored, stored2)
	}
}

// TestTxManagerRefreshStatusesResolvesBroadcastUnknownBySameBytes is the
// other half of the ambiguous-broadcast behavior: once the transport
// recovers, RefreshStatuses must resolve a TxBroadcastUnknown record by
// rebroadcasting the EXACT SAME persisted bytes (same hash, same nonce) —
// never by re-signing or allocating a new nonce — and then, once a receipt
// appears, advance it normally.
func TestTxManagerRefreshStatusesResolvesBroadcastUnknownBySameBytes(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.SendTransactionErr = context.DeadlineExceeded
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	if _, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "key-resolve", PrivateKey: priv, To: to}); err == nil {
		t.Fatal("expected Submit to error while ambiguous")
	}
	before, err := txRepo.GetByIdempotencyKey(context.Background(), "key-resolve")
	if err != nil {
		t.Fatal(err)
	}

	// Transport recovers; RefreshStatuses should rebroadcast the same bytes
	// (no receipt exists yet, so it stays TxBroadcastUnknown, but the
	// PendingNonceAt-allocating Submit path must never be involved).
	client.SendTransactionErr = nil
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if len(client.SentTxs) != 1 {
		t.Fatalf("expected exactly 1 rebroadcast, got %d", len(client.SentTxs))
	}
	if client.SentTxs[0].Hash() != common.HexToHash(before.TxHash) {
		t.Fatalf("rebroadcast hash = %s, want the original %s (same signed bytes)", client.SentTxs[0].Hash(), before.TxHash)
	}
	if client.SentTxs[0].Nonce() != before.Nonce {
		t.Fatalf("rebroadcast nonce = %d, want the original %d", client.SentTxs[0].Nonce(), before.Nonce)
	}
	afterRebroadcast, err := txRepo.Get(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRebroadcast.Status != models.TxBroadcastUnknown {
		t.Fatalf("Status = %s, want still %s until a receipt is observed", afterRebroadcast.Status, models.TxBroadcastUnknown)
	}

	// A receipt now appears: RefreshStatuses resolves it normally.
	client.SetReceipt(common.HexToHash(before.TxHash), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(int64(client.BlockNum))})
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	resolved, err := txRepo.Get(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != models.TxMined {
		t.Fatalf("Status = %s, want mined", resolved.Status)
	}
	if len(client.SentTxs) != 1 {
		t.Fatalf("no further rebroadcast should happen once a receipt is observed, got %d sent", len(client.SentTxs))
	}
}

// TestTxManagerReplaceRejectsFeeCapExceededAfterBump is the "fee-cap
// violation after bump rejected" regression: buildTx's own pre-bump check
// passes (the base fee is within cap), but bumpPercent pushes the bumped
// fee over the configured MaxFeePerGas — Replace must reject it and leave
// the original transaction untouched and unbroadcast.
func TestTxManagerReplaceRejectsFeeCapExceededAfterBump(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.GasPrice = big.NewInt(1_000_000_000) // within the cap below, pre-bump
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManagerWithFeeCaps(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{
		MaxFeePerGas: big.NewInt(1_100_000_000), // a 20% bump pushes 1e9 -> 1.2e9, past this cap
	})
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(client.SentTxs) != 1 {
		t.Fatalf("expected the original submission to broadcast once, got %d", len(client.SentTxs))
	}

	if _, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20); err == nil {
		t.Fatal("expected Replace to reject a bumped fee above MaxFeePerGas")
	}
	if len(client.SentTxs) != 1 {
		t.Fatalf("a rejected bump must never broadcast, got %d sent", len(client.SentTxs))
	}
	stillOriginal, err := txRepo.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillOriginal.Status != models.TxPending {
		t.Fatalf("original Status = %s, want still pending after a rejected replacement", stillOriginal.Status)
	}
	if stillOriginal.ReplacedBy != "" {
		t.Fatalf("original must not be linked to a replacement that was never signed/broadcast, got ReplacedBy=%q", stillOriginal.ReplacedBy)
	}
}

// TestTxManagerReplaceAmbiguousBroadcastRecoversViaRebroadcast is the
// "replacement crash-after-broadcast recovery" regression: Replace persists
// the replacement record AND marks the original Replaced BEFORE
// broadcasting. If the broadcast itself then fails ambiguously (transport
// error, outcome unknown — the closest a single-process unit test can get
// to "crashed right after the send call"), the replacement record must
// stay durable (TxBroadcastUnknown, exact signed bytes persisted) and a
// later RefreshStatuses must recover it by rebroadcasting those same bytes,
// never a new nonce/signature.
func TestTxManagerReplaceAmbiguousBroadcastRecoversViaRebroadcast(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}

	client.SendTransactionErr = context.DeadlineExceeded
	if _, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20); err == nil {
		t.Fatal("expected Replace to error while the replacement broadcast outcome is unknown")
	}

	// The replacement intent (and the original's Replaced link) must
	// already be durable even though the broadcast itself is unresolved.
	updatedOriginal, err := txRepo.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedOriginal.Status != models.TxReplaced || updatedOriginal.ReplacedBy == "" {
		t.Fatalf("original = %+v, want Replaced with a ReplacedBy link persisted before the ambiguous broadcast", updatedOriginal)
	}
	replacement, err := txRepo.Get(context.Background(), updatedOriginal.ReplacedBy)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Status != models.TxBroadcastUnknown {
		t.Fatalf("replacement Status = %s, want %s", replacement.Status, models.TxBroadcastUnknown)
	}
	if replacement.SignedRawTx == "" {
		t.Fatal("expected the replacement's exact signed bytes to be persisted")
	}
	if replacement.Nonce != original.Nonce {
		t.Fatalf("replacement nonce = %d, want %d (same nonce)", replacement.Nonce, original.Nonce)
	}

	client.SendTransactionErr = nil
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	// SentTxs[0] is the ORIGINAL transaction's own successful broadcast
	// (from Submit, before Replace was ever called); SentTxs[1] must be
	// exactly one rebroadcast of the replacement's persisted bytes.
	if len(client.SentTxs) != 2 {
		t.Fatalf("expected the original broadcast plus exactly 1 rebroadcast of the replacement, got %d sent", len(client.SentTxs))
	}
	last := client.SentTxs[len(client.SentTxs)-1]
	if last.Hash() != common.HexToHash(replacement.TxHash) || last.Nonce() != replacement.Nonce {
		t.Fatalf("rebroadcast must use the replacement's exact signed bytes/nonce, got hash=%s nonce=%d", last.Hash(), last.Nonce())
	}
}

// TestTxManagerReconcileReplacementsRepairsMissingLink is the
// "reconcile incomplete replacement records" regression: if the original
// transaction's Replaced/ReplacedBy update never happened (simulating a
// crash between persisting the replacement record and linking the
// original), RefreshStatuses must self-heal the link using the
// replacement's Replaces back-reference.
func TestTxManagerReconcileReplacementsRepairsMissingLink(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a replacement record that was persisted but whose original
	// was never marked Replaced (the crash window reconcileReplacements
	// exists to repair).
	orphan := &models.Transaction{
		ID: "replacement-orphan", Kind: original.Kind, ChainID: original.ChainID,
		From: original.From, To: original.To, Data: original.Data, Value: original.Value,
		Nonce: original.Nonce, TxHash: "0x" + common.Bytes2Hex([]byte("fake-replacement-hash-000000000")),
		SignedRawTx: "0x00", Replaces: original.ID, Status: models.TxPending,
	}
	if err := txRepo.Create(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}

	stillPending, err := txRepo.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != models.TxPending {
		t.Fatalf("precondition: original should still look Pending, got %s", stillPending.Status)
	}

	// RefreshStatuses needs a resolvable receipt lookup for the orphan's
	// bogus hash; give it a receipt so refreshOne doesn't error trying to
	// rebroadcast undecodable bytes.
	client.SetReceipt(common.HexToHash(orphan.TxHash), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(int64(client.BlockNum))})

	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	repaired, err := txRepo.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Status != models.TxReplaced || repaired.ReplacedBy != orphan.ID {
		t.Fatalf("original = %+v, want Replaced with ReplacedBy=%q", repaired, orphan.ID)
	}
}

// TestTxManagerReconcileReplacementsIgnoresFailedReplacement checks that a
// FAILED replacement must not let reconcileReplacements mark its original
// Replaced. A replacement record
// whose OWN Status is TxFailed never actually reached the mempool — it
// must never cause reconcileReplacements to (re-)mark a live, still-Pending
// original as Replaced.
func TestTxManagerReconcileReplacementsIgnoresFailedReplacement(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}

	// A TxFailed replacement record that still carries Replaces (as
	// Replace's own definite-rejection branch would produce if its
	// best-effort revert-to-Pending write on `original` were somehow lost
	// — e.g. a crash right after persisting newRecord as Failed).
	failedReplacement := &models.Transaction{
		ID: "replacement-failed", Kind: original.Kind, ChainID: original.ChainID,
		From: original.From, To: original.To, Data: original.Data, Value: original.Value,
		Nonce: original.Nonce, TxHash: "0x" + common.Bytes2Hex([]byte("fake-failed-replacement-hash-00")),
		SignedRawTx: "0x00", Replaces: original.ID, Status: models.TxFailed,
	}
	if err := txRepo.Create(context.Background(), failedReplacement); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	stillPending, err := txRepo.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != models.TxPending {
		t.Fatalf("original Status = %s, want still Pending — a TxFailed replacement must never mark it Replaced", stillPending.Status)
	}
	if stillPending.ReplacedBy != "" {
		t.Fatalf("original ReplacedBy = %q, want empty — a TxFailed replacement must never link itself", stillPending.ReplacedBy)
	}
}

// TestTxManagerRefreshStatusesConfirmedTxReorgsOnMissingReceipt covers the
// core reorg case: previously, once a transaction reached TxConfirmed,
// RefreshStatuses skipped it forever. Now a Confirmed transaction whose
// receipt later disappears (the classic reorg symptom) must transition to
// TxReorged, using the already-defined-but-previously-unused state.
func TestTxManagerRefreshStatusesConfirmedTxReorgsOnMissingReceipt(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	txHash := common.HexToHash(rec.TxHash)
	client.SetReceipt(txHash, &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(int64(client.BlockNum))})
	client.BlockNum += 3
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, err := txRepo.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TxConfirmed {
		t.Fatalf("precondition: Status = %s, want confirmed", got.Status)
	}

	// The block (and this transaction's receipt) gets reorged out.
	client.RemoveReceipt(txHash)
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, err = txRepo.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TxReorged {
		t.Fatalf("Status = %s, want reorged after a post-confirmation reorg", got.Status)
	}
}

// TestTxManagerRefreshStatusesConfirmedTxReorgsOnBlockHashMismatch covers
// the other reorg signal: the receipt is still returned, but the
// CURRENT canonical header at that height no longer has the hash the
// receipt originally recorded (a shallower/alternate reorg where a
// DIFFERENT block ended up canonical at the same height, rather than the
// height disappearing outright).
func TestTxManagerRefreshStatusesConfirmedTxReorgsOnBlockHashMismatch(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	txHash := common.HexToHash(rec.TxHash)
	minedAt := big.NewInt(int64(client.BlockNum))
	// Pin the pre-reorg canonical header so its hash is known, and record
	// that same hash on the receipt — exactly what a real node's receipt
	// would contain for a transaction mined in this (still canonical, for
	// now) block.
	preReorgHeader := &types.Header{Number: minedAt, Extra: []byte("pre-reorg")}
	client.SetHeader(minedAt.Uint64(), preReorgHeader)
	client.SetReceipt(txHash, &types.Receipt{
		Status: types.ReceiptStatusSuccessful, BlockNumber: minedAt, BlockHash: preReorgHeader.Hash(),
	})
	client.BlockNum += 3
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, err := txRepo.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TxConfirmed {
		t.Fatalf("precondition: Status = %s, want confirmed", got.Status)
	}

	// A reorg replaces the canonical block at that same height with a
	// different one — the receipt (still returned, unchanged) now refers
	// to a non-canonical block.
	client.SetHeader(minedAt.Uint64(), &types.Header{Number: minedAt, Extra: []byte("post-reorg-fork")})
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, err = txRepo.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.TxReorged {
		t.Fatalf("Status = %s, want reorged after a canonical block-hash mismatch", got.Status)
	}
}

func TestTxManagerSubmitBuildsEIP1559AndPersists(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(priv.PublicKey)

	client := NewFakeClient()
	client.SetNonce(from, 5)
	client.Receipts = map[common.Hash]*types.Receipt{} // no receipt yet

	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeEIP1559)

	to := common.HexToAddress("0x00000000000000000000000000000000000042")
	rec, err := mgr.Submit(context.Background(), SubmitRequest{
		Kind:       "compliance.setStatus",
		PrivateKey: priv,
		To:         to,
		Data:       []byte{0xde, 0xad, 0xbe, 0xef},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if rec.Status != models.TxPending {
		t.Errorf("Status = %s, want pending", rec.Status)
	}
	if rec.Nonce != 5 {
		t.Errorf("Nonce = %d, want 5", rec.Nonce)
	}
	if len(client.SentTxs) != 1 {
		t.Fatalf("expected 1 sent tx, got %d", len(client.SentTxs))
	}
	if client.SentTxs[0].Type() != types.DynamicFeeTxType {
		t.Errorf("expected DynamicFeeTx, got type %d", client.SentTxs[0].Type())
	}

	stored, err := txRepo.Get(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.TxHash != rec.TxHash {
		t.Errorf("stored TxHash mismatch")
	}
}

func TestTxManagerSubmitFallsBackToLegacyWithoutBaseFee(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.BaseFee = nil // simulate a pre-London chain with no EIP-1559 support
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeEIP1559)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_ = rec
	if client.SentTxs[0].Type() != types.LegacyTxType {
		t.Errorf("expected legacy fallback, got type %d", client.SentTxs[0].Type())
	}
}

func TestTxManagerSubmitLegacyMode(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	_, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if client.SentTxs[0].Type() != types.LegacyTxType {
		t.Errorf("expected legacy tx, got type %d", client.SentTxs[0].Type())
	}
}

func TestTxManagerSubmitIsIdempotent(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	req := SubmitRequest{IdempotencyKey: "key-123", PrivateKey: priv, To: to, Data: []byte{1, 2, 3}}

	rec1, err := mgr.Submit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := mgr.Submit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rec1.ID != rec2.ID || rec1.TxHash != rec2.TxHash {
		t.Errorf("expected identical result for repeated idempotency key, got %+v vs %+v", rec1, rec2)
	}
	if len(client.SentTxs) != 1 {
		t.Errorf("expected exactly 1 broadcast, got %d", len(client.SentTxs))
	}
}

// TestTxManagerSubmitPersistsBeforeBroadcast guards against "broadcast
// precedes repository Create": a SendTransaction failure
// must never leave the record un-persisted, and the record it does leave
// must be TxFailed (retryable), not TxPending (which would look like an
// in-flight transaction that was never actually sent).
func TestTxManagerSubmitPersistsBeforeBroadcast(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.SendTransactionErr = errors.New("boom: node rejected the transaction")
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	_, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "key-fail", PrivateKey: priv, To: to})
	if err == nil {
		t.Fatal("expected Submit to fail when SendTransaction errors")
	}
	if len(client.SentTxs) != 0 {
		t.Fatalf("SendTransaction should not have recorded a sent tx on error, got %d", len(client.SentTxs))
	}

	stored, gerr := txRepo.GetByIdempotencyKey(context.Background(), "key-fail")
	if gerr != nil {
		t.Fatalf("expected a persisted record even though broadcast failed: %v", gerr)
	}
	if stored.Status != models.TxFailed {
		t.Errorf("Status = %s, want %s", stored.Status, models.TxFailed)
	}
}

// TestTxManagerSubmitRetriesAfterBroadcastFailure is the other half: a
// TxFailed record under a given idempotency key must NOT permanently wedge
// that key — a later Submit call with the same key must actually resubmit
// (and, on success, overwrite the same record id) rather than replaying the
// failed result forever.
func TestTxManagerSubmitRetriesAfterBroadcastFailure(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.SendTransactionErr = errors.New("boom")
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	req := SubmitRequest{IdempotencyKey: "key-retry", PrivateKey: priv, To: to}

	if _, err := mgr.Submit(context.Background(), req); err == nil {
		t.Fatal("expected first Submit to fail")
	}
	failedID, err := txRepo.GetByIdempotencyKey(context.Background(), "key-retry")
	if err != nil {
		t.Fatal(err)
	}

	client.SendTransactionErr = nil // node recovers
	rec, err := mgr.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if rec.Status != models.TxPending {
		t.Errorf("Status = %s, want pending", rec.Status)
	}
	if rec.ID != failedID.ID {
		t.Errorf("retry should reuse the same transaction record id, got %s want %s", rec.ID, failedID.ID)
	}
	if len(client.SentTxs) != 1 {
		t.Errorf("expected exactly 1 successful broadcast, got %d", len(client.SentTxs))
	}
}

// TestTxManagerSubmitRejectsFeeAboveCap is the fee-cap enforcement test.
func TestTxManagerSubmitRejectsFeeAboveCap(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.GasPrice = big.NewInt(1_000_000_000_000) // way above the cap below
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManagerWithFeeCaps(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{
		MaxFeePerGas: big.NewInt(2_000_000_000),
	})

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	_, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err == nil {
		t.Fatal("expected Submit to reject a gas price above MaxFeePerGas")
	}
	if len(client.SentTxs) != 0 {
		t.Errorf("expected no broadcast for a rejected fee, got %d", len(client.SentTxs))
	}
}

// TestTxManagerSubmitAllowsFeeWithinCap is the positive counterpart.
func TestTxManagerSubmitAllowsFeeWithinCap(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManagerWithFeeCaps(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{
		MaxFeePerGas: big.NewInt(1_000_000_000_000),
	})

	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	if _, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(client.SentTxs) != 1 {
		t.Errorf("expected 1 broadcast, got %d", len(client.SentTxs))
	}
}

func TestTxManagerSubmitSerializesNoncesPerKey(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	client.SetNonce(from, 0)
	// AutoIncrementNonce simulates a real node's mempool, where a pending-
	// nonce lookup already reflects every broadcast-but-unconfirmed tx —
	// including one from a racing concurrent caller. Without this, the only
	// way to advance the fake nonce is a manual post-Submit SetNonce call,
	// but that necessarily happens after TxManager's own per-key lock has
	// already been released, leaving a real window for a second Submit to
	// read the same stale nonce before the first goroutine gets around to
	// bumping it — a race in the test's own simulation, not in TxManager,
	// but one that occasionally flakes this test under -race's scheduling.
	client.AutoIncrementNonce = true
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	var wg sync.WaitGroup
	const n = 10
	results := make([]uint64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to, Data: []byte{byte(i)}})
			if err != nil {
				t.Errorf("Submit[%d]: %v", i, err)
				return
			}
			results[i] = rec.Nonce
		}(i)
	}
	wg.Wait()

	seen := map[uint64]bool{}
	for _, nonce := range results {
		if seen[nonce] {
			t.Fatalf("nonce %d used more than once: %v", nonce, results)
		}
		seen[nonce] = true
	}
}

func TestTxManagerRefreshStatusesTransitions(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	txHash := common.HexToHash(rec.TxHash)

	// No receipt yet: stays pending.
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ := txRepo.Get(context.Background(), rec.ID)
	if got.Status != models.TxPending {
		t.Fatalf("Status = %s, want pending", got.Status)
	}

	// Receipt appears at block 100 (current block), success, but not enough
	// confirmations yet.
	client.SetReceipt(txHash, &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(int64(client.BlockNum))})
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ = txRepo.Get(context.Background(), rec.ID)
	if got.Status != models.TxMined {
		t.Fatalf("Status = %s, want mined", got.Status)
	}

	// Chain advances past confirmations threshold.
	client.BlockNum += 3
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ = txRepo.Get(context.Background(), rec.ID)
	if got.Status != models.TxConfirmed {
		t.Fatalf("Status = %s, want confirmed", got.Status)
	}
}

func TestTxManagerRefreshStatusesMarksReverted(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	client.SetReceipt(common.HexToHash(rec.TxHash), &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: big.NewInt(int64(client.BlockNum))})

	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ := txRepo.Get(context.Background(), rec.ID)
	if got.Status != models.TxReverted {
		t.Fatalf("Status = %s, want reverted", got.Status)
	}
}

func TestTxManagerReplaceBumpsFeeAndMarksOriginalReplaced(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}

	replacement, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if replacement.Nonce != original.Nonce {
		t.Errorf("replacement nonce = %d, want %d (same nonce)", replacement.Nonce, original.Nonce)
	}
	if len(client.SentTxs) != 2 {
		t.Fatalf("expected 2 broadcasts, got %d", len(client.SentTxs))
	}
	origGasPrice := client.SentTxs[0].GasPrice()
	newGasPrice := client.SentTxs[1].GasPrice()
	if newGasPrice.Cmp(origGasPrice) <= 0 {
		t.Errorf("replacement gas price %s not greater than original %s", newGasPrice, origGasPrice)
	}

	updatedOriginal, err := txRepo.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedOriginal.Status != models.TxReplaced {
		t.Errorf("original status = %s, want replaced", updatedOriginal.Status)
	}
	if updatedOriginal.ReplacedBy != replacement.ID {
		t.Errorf("ReplacedBy = %q, want %q", updatedOriginal.ReplacedBy, replacement.ID)
	}
}

// TestBumpFeeFieldRoundsUpAndBeatsOriginal covers the rounding edge:
// floor(original*(100+bump)/100) can equal the original for
// small values (an underpriced replacement the node rejects); ceiling
// arithmetic plus the +1 guard must always strictly exceed it.
func TestBumpFeeFieldRoundsUpAndBeatsOriginal(t *testing.T) {
	cases := []struct {
		name              string
		original, current *big.Int
		bump              int64
		wantAtLeast       *big.Int
	}{
		// floor(5*101/100)=5 == original; ceil=6.
		{"rounding beats original", big.NewInt(5), big.NewInt(5), 1, big.NewInt(6)},
		// current fell far below original: bump from the original, not current.
		{"falling fee bumps from original", big.NewInt(100), big.NewInt(1), 10, big.NewInt(110)},
		// no decodable original: bump the current suggestion only.
		{"nil original bumps current", nil, big.NewInt(100), 10, big.NewInt(110)},
		// a caller-supplied bump BELOW the chain replacement minimum (10%) is
		// floored up to it against the original.
		{"sub-minimum bump floored to 10pct", big.NewInt(1000), big.NewInt(1000), 5, big.NewInt(1100)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bumpFeeField(tc.original, tc.current, tc.bump)
			if got.Cmp(tc.wantAtLeast) < 0 {
				t.Errorf("bumpFeeField(%v,%v,%d) = %s, want >= %s", tc.original, tc.current, tc.bump, got, tc.wantAtLeast)
			}
			if tc.original != nil && got.Cmp(tc.original) <= 0 {
				t.Errorf("bumpFeeField(%v,%v,%d) = %s, must strictly exceed original %s", tc.original, tc.current, tc.bump, got, tc.original)
			}
		})
	}
}

// TestTxManagerReplaceBumpsFromOriginalWhenFeesFell covers the falling-fee
// case: when the current network suggestion has dropped
// below the original attempt's fee, the replacement must still beat the
// ORIGINAL (otherwise the node rejects it as underpriced), not merely bump a
// now-lower fresh quote.
func TestTxManagerReplaceBumpsFromOriginalWhenFeesFell(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.GasPrice = big.NewInt(100_000_000_000) // high at submit time
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	origGasPrice := client.SentTxs[0].GasPrice()

	// Network fees collapse before the replacement.
	client.GasPrice = big.NewInt(1)

	if _, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	newGasPrice := client.SentTxs[1].GasPrice()
	if newGasPrice.Cmp(origGasPrice) <= 0 {
		t.Errorf("replacement gas price %s not greater than original %s despite fallen network fees", newGasPrice, origGasPrice)
	}
}

func TestTxManagerReplaceRejectsWrongKey(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	otherKey, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(otherKey), 20); err == nil {
		t.Fatal("expected error when replacement key does not match original sender")
	}
}

// --- Nonce reconciliation, external consumption, stuck-tx intervention ---

// TestTxManagerAllocateNonceReconcilesAgainstLatest is the "RPC providers
// returning inconsistent pending-nonce results" scenario: a stale/lagging
// node reports a PENDING nonce below the account's true LATEST (mined)
// nonce. Submit must never sign at a nonce below latest (that would be an
// immediate "nonce too low"), so it must use latest instead of blindly
// trusting the pending view.
func TestTxManagerAllocateNonceReconcilesAgainstLatest(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	client.SetNonce(from, 2)      // stale/lagging pending view
	client.SetMinedNonce(from, 5) // ground truth: 5 transactions already mined
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	tx, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Nonce != 5 {
		t.Fatalf("Nonce = %d, want 5 (reconciled against latest, not the stale pending view)", tx.Nonce)
	}
}

// TestTxManagerAllocateNonceReconcilesAgainstLocalRecords covers the
// opposite direction: this manager's own outstanding local record for a
// nonce the node's pending view doesn't (yet) reflect must still be
// respected, so a fresh Submit does not collide with it.
func TestTxManagerAllocateNonceReconcilesAgainstLocalRecords(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	client.SetNonce(from, 0)
	client.SetMinedNonce(from, 0)
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	// A locally tracked TxPending record at nonce 3 that the fake node's
	// pending/latest views don't know about yet.
	if err := txRepo.Create(context.Background(), &models.Transaction{
		ID: "local-1", From: from.Hex(), To: to.Hex(), Nonce: 3, Status: models.TxPending,
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Nonce != 4 {
		t.Fatalf("Nonce = %d, want 4 (one past the highest locally tracked outstanding nonce)", tx.Nonce)
	}
}

// TestTxManagerRefreshStatusesDetectsExternallyConsumedNonce: a TxPending
// record whose nonce the account's on-chain (latest/mined) count has
// already passed, with no receipt ever observed for that record's own
// hash, means some OTHER transaction was mined at that nonce. RefreshStatuses
// must mark it TxNonceConsumedExternally rather than leaving
// it TxPending forever, and its Idempotency-Key must become retryable.
func TestTxManagerRefreshStatusesDetectsExternallyConsumedNonce(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	tx, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "ext-consumed", PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status != models.TxPending {
		t.Fatalf("Status = %s, want pending", tx.Status)
	}

	// Simulate some other transaction (not TxManager's) getting mined at
	// this same nonce: the account's latest/mined count advances past it,
	// but no receipt exists for THIS transaction's hash.
	client.SetMinedNonce(from, tx.Nonce+1)

	if err := mgr.RefreshStatuses(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	updated, err := txRepo.Get(context.Background(), tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.TxNonceConsumedExternally {
		t.Fatalf("Status = %s, want %s", updated.Status, models.TxNonceConsumedExternally)
	}

	// By default (AllowReorgRetry unset — the correct
	// default for a Kind whose on-chain idempotence the generic manager
	// cannot verify), the SAME idempotency key must NOT silently retry —
	// an unknown replacement may have already performed this business
	// action — and this signer must be blocked entirely until an operator
	// resolves it, exactly like an explicit TxNeedsIntervention record.
	client.SetNonce(from, tx.Nonce+1)
	client.SetMinedNonce(from, tx.Nonce+1)
	if _, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "ext-consumed", PrivateKey: priv, To: to}); err == nil {
		t.Fatal("expected Submit to refuse a same-key retry after nonce-consumed-externally by default")
	}
	if _, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "unrelated-key", PrivateKey: priv, To: to}); err == nil {
		t.Fatal("expected Submit to block ANY submission for this signer, not just the same idempotency key, until resolved")
	}
}

// TestTxManagerSubmitAllowsReorgRetryWhenOptedIn is the opt-in counterpart:
// a Kind that explicitly sets AllowReorgRetry (e.g.
// compliance.StatusService's setStatus, which is safely repeatable) may
// resubmit under the same idempotency key after
// TxNonceConsumedExternally/TxReorged, and does NOT block the signer.
func TestTxManagerSubmitAllowsReorgRetryWhenOptedIn(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	tx, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "ext-consumed-2", PrivateKey: priv, To: to, AllowReorgRetry: true})
	if err != nil {
		t.Fatal(err)
	}
	client.SetMinedNonce(from, tx.Nonce+1)
	if err := mgr.RefreshStatuses(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	updated, err := txRepo.Get(context.Background(), tx.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.TxNonceConsumedExternally {
		t.Fatalf("Status = %s, want %s", updated.Status, models.TxNonceConsumedExternally)
	}

	client.SetNonce(from, tx.Nonce+1)
	retried, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "ext-consumed-2", PrivateKey: priv, To: to, AllowReorgRetry: true})
	if err != nil {
		t.Fatalf("expected AllowReorgRetry to permit a same-key retry, got %v", err)
	}
	if retried.ID != tx.ID {
		t.Fatalf("expected the retry to reuse the same record ID, got %s vs %s", retried.ID, tx.ID)
	}
	if retried.Status == models.TxNonceConsumedExternally {
		t.Fatal("expected Submit to actually resubmit, not just return the stale consumed record")
	}
	if len(retried.Attempts) != 1 || retried.Attempts[0].Status != models.TxNonceConsumedExternally {
		t.Fatalf("expected the prior consumed attempt retained in Attempts, got %+v", retried.Attempts)
	}
}

// TestTxManagerReplaceExhaustsAttemptsMarksNeedsIntervention: once a
// transaction's replacement attempts hit the configured cap, Replace must
// stop signing/broadcasting anything further for it and instead mark it
// TxNeedsIntervention.
func TestTxManagerReplaceExhaustsAttemptsMarksNeedsIntervention(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManagerWithPolicy(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{}, 1)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}

	// First replacement is within the cap (maxReplacementAttempts=1) and succeeds.
	replacement, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20)
	if err != nil {
		t.Fatalf("first replacement should succeed: %v", err)
	}
	sentBeforeSecond := len(client.SentTxs)

	// Second replacement of the (already-replaced) chain: attempt it on
	// the replacement record itself, which is TxPending and carries
	// ReplacementCount 1, exceeding the cap of 1.
	if _, err := mgr.Replace(context.Background(), replacement.ID, NewStaticKeySigner(priv), 20); err == nil {
		t.Fatal("expected an error once the replacement-attempt cap is exceeded")
	}
	if len(client.SentTxs) != sentBeforeSecond {
		t.Fatalf("expected no further broadcast once the cap is exceeded, sent %d -> %d", sentBeforeSecond, len(client.SentTxs))
	}

	stuck, err := txRepo.Get(context.Background(), replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stuck.Status != models.TxNeedsIntervention {
		t.Fatalf("Status = %s, want %s", stuck.Status, models.TxNeedsIntervention)
	}
}

// TestTxManagerSubmitBlockedBySignerNeedsIntervention: while ANY transaction
// for a signer is TxNeedsIntervention, Submit must refuse new submissions
// for that same signer, to prevent later nonce-dependent transactions from
// being incorrectly reported as finalized.
func TestTxManagerSubmitBlockedBySignerNeedsIntervention(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	if err := txRepo.Create(context.Background(), &models.Transaction{
		ID: "stuck-1", From: from.Hex(), To: to.Hex(), Nonce: 0, Status: models.TxNeedsIntervention,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to}); err == nil {
		t.Fatal("expected Submit to refuse a signer with an outstanding TxNeedsIntervention transaction")
	}
	if len(client.SentTxs) != 0 {
		t.Fatalf("expected nothing broadcast, got %d", len(client.SentTxs))
	}
}

// --- Distributed nonce lease ("mongo-lease" mode) ---

// TestTxManagerLeaseMutualExclusion is the core multi-replica scenario:
// two TxManager instances sharing ONE NonceLeaseRepository store (as two
// server replicas sharing one Mongo deployment would) for the SAME signer.
// Replica A already holds the lease (simulated directly against the shared
// store, standing in for "A's Submit is mid-flight"); replica B's Submit
// must fail fast with ErrLeaseUnavailable — no nonce allocated, no
// transaction record persisted, nothing broadcast.
func TestTxManagerLeaseMutualExclusion(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	leases := memory.NewNonceLeaseRepository()

	key := "31337:" + from.Hex()
	if _, ok, err := leases.Acquire(context.Background(), key, "replica-A", time.Minute); err != nil || !ok {
		t.Fatalf("replica-A Acquire: ok=%v err=%v", ok, err)
	}

	mgrB := NewTxManagerWithLease(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{}, 0, leases, time.Minute)
	_, err := mgrB.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatalf("expected ErrLeaseUnavailable, got %v", err)
	}
	if len(client.SentTxs) != 0 {
		t.Fatalf("expected nothing broadcast, got %d", len(client.SentTxs))
	}
	all, err := txRepo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("expected no transaction record allocated/persisted, got %d", len(all))
	}
}

// TestTxManagerLeaseAllowsSubmitOnceHeld proves the flip side of mutual
// exclusion: once a signer's lease is free, a "mongo-lease"-mode TxManager
// acquires it and Submit proceeds normally, stamping the record with the
// acquired fencing token.
func TestTxManagerLeaseAllowsSubmitOnceHeld(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	leases := memory.NewNonceLeaseRepository()

	mgr := NewTxManagerWithLease(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{}, 0, leases, time.Minute)
	tx, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if tx.FencingToken == 0 {
		t.Fatal("expected a non-zero fencing token stamped on the record in mongo-lease mode")
	}
	if len(client.SentTxs) != 1 {
		t.Fatalf("expected one broadcast, got %d", len(client.SentTxs))
	}
}

// TestNonceLeaseTakeoverAfterExpiryStrictlyIncrementsToken covers two
// required acceptance criteria directly against the lease store: an
// expired lease is taken over by another holder with a STRICTLY greater
// fencing token, and the superseded holder's stale (holder,token) pair is
// rejected by Renew afterward — "a superseded holder must not overwrite."
func TestNonceLeaseTakeoverAfterExpiryStrictlyIncrementsToken(t *testing.T) {
	leases := memory.NewNonceLeaseRepository()
	key := "31337:0x0000000000000000000000000000000000bEEF"

	tokenA, ok, err := leases.Acquire(context.Background(), key, "replica-A", -time.Millisecond) // immediately expired
	if err != nil || !ok {
		t.Fatalf("replica-A Acquire: ok=%v err=%v", ok, err)
	}

	tokenB, ok, err := leases.Acquire(context.Background(), key, "replica-B", time.Minute)
	if err != nil || !ok {
		t.Fatalf("expected replica-B to take over the expired lease: ok=%v err=%v", ok, err)
	}
	if tokenB <= tokenA {
		t.Fatalf("expected a strictly increasing fencing token, got A=%d B=%d", tokenA, tokenB)
	}

	// The stale holder's guarded write must be rejected: renewing with the
	// OLD (holder, token) pair fails now that replica-B holds the lease.
	renewed, err := leases.Renew(context.Background(), key, "replica-A", tokenA, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed {
		t.Fatal("expected replica-A's renew to fail after replica-B's takeover")
	}

	// replica-B, the current holder, renews successfully.
	renewed, err = leases.Renew(context.Background(), key, "replica-B", tokenB, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed {
		t.Fatal("expected replica-B's renew to succeed")
	}
}

// TestNonceLeaseReleaseFreesLeaseImmediately proves a well-behaved holder's
// Release lets another replica acquire immediately, without waiting out
// the TTL, and that Release itself is fenced (wrong holder/token rejected).
func TestNonceLeaseReleaseFreesLeaseImmediately(t *testing.T) {
	leases := memory.NewNonceLeaseRepository()
	key := "31337:0x0000000000000000000000000000000000bEEF"

	tokenA, ok, err := leases.Acquire(context.Background(), key, "replica-A", time.Hour)
	if err != nil || !ok {
		t.Fatalf("replica-A Acquire: ok=%v err=%v", ok, err)
	}

	if _, ok, err := leases.Acquire(context.Background(), key, "replica-B", time.Hour); err != nil || ok {
		t.Fatalf("expected replica-B to be blocked while A's lease is live: ok=%v err=%v", ok, err)
	}

	if err := leases.Release(context.Background(), key, "replica-A", tokenA); err != nil {
		t.Fatalf("Release: %v", err)
	}

	tokenB, ok, err := leases.Acquire(context.Background(), key, "replica-B", time.Hour)
	if err != nil || !ok {
		t.Fatalf("expected replica-B to acquire immediately after A's release: ok=%v err=%v", ok, err)
	}
	if tokenB <= tokenA {
		t.Fatalf("expected a strictly increasing fencing token, got A=%d B=%d", tokenA, tokenB)
	}

	if err := leases.Release(context.Background(), key, "replica-A", tokenA); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Fatalf("expected ErrFencingTokenMismatch for a stale/superseded release, got %v", err)
	}
}

// TestTxManagerInProcessModeUnaffectedByLeaseFeature is a smoke check that
// a plain (non-lease) TxManager, constructed exactly as before this
// follow-up, needs no NonceLeaseRepository at all and behaves identically —
// the full pre-existing test suite in this file already proves this in
// depth; this just makes the "in-process mode is unchanged" claim explicit
// in one place.
func TestTxManagerInProcessModeUnaffectedByLeaseFeature(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")
	client := NewFakeClient()
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy) // no lease store involved anywhere

	tx, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if tx.FencingToken != 0 {
		t.Fatalf("expected FencingToken 0 in in-process mode, got %d", tx.FencingToken)
	}
}

// versionRacingRepo wraps a repository.TransactionRepository and, the FIRST
// time Get is called for a specific ID, also bumps that record's Version
// via a harmless self-referential UpdateConditional before returning —
// deterministically simulating "another writer raced in and modified this
// record between the caller's read and its later conditional write"
// without needing real goroutines (which, given Replace's own in-process
// per-signer lock, could never actually race within a single txManager
// anyway — this reproduces the cross-replica case the lock does NOT cover).
type versionRacingRepo struct {
	repository.TransactionRepository
	raceOnGetID string
	raced       bool
}

func (r *versionRacingRepo) Get(ctx context.Context, id string) (*models.Transaction, error) {
	tx, err := r.TransactionRepository.Get(ctx, id)
	if err != nil || r.raced || id != r.raceOnGetID {
		return tx, err
	}
	r.raced = true
	cp := *tx
	cp.UpdatedAt = time.Now().UTC()
	if _, err := r.TransactionRepository.UpdateConditional(ctx, &cp, tx.Version); err != nil {
		return nil, err
	}
	return tx, nil
}

// TestTxManagerReplaceRejectsConcurrentlyModifiedOriginal exercises the
// conditional write: stale tokens are rejected at the repository write
// itself, not only in a prior Renew call. A writer whose
// read of `old` is stale by the time it tries to mark it Replaced must be
// rejected by UpdateConditional and must NOT broadcast its replacement.
func TestTxManagerReplaceRejectsConcurrentlyModifiedOriginal(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	backing := memory.NewTransactionRepository()
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	mgr := NewTxManager(client, backing, big.NewInt(31337), FeeModeLegacy)
	original, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: to})
	if err != nil {
		t.Fatal(err)
	}

	racing := &versionRacingRepo{TransactionRepository: backing, raceOnGetID: original.ID}
	racingMgr := NewTxManager(client, racing, big.NewInt(31337), FeeModeLegacy)

	sentBefore := len(client.SentTxs)
	if _, err := racingMgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20); err == nil {
		t.Fatal("expected Replace to reject a concurrently-modified original")
	}
	if len(client.SentTxs) != sentBefore {
		t.Fatalf("expected no broadcast when the conditional write loses the race, sent %d -> %d", sentBefore, len(client.SentTxs))
	}

	// The original itself must be untouched by the losing attempt (still
	// whatever the injected "concurrent" writer left it as: Pending).
	stillPending, err := backing.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != models.TxPending {
		t.Fatalf("original Status = %s, want still Pending", stillPending.Status)
	}
}
