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

// --- Broadcast and receipt lifecycle tests ---

// TestTxManagerSubmitAmbiguousBroadcastMarksUnknown checks that a
// transport-level SendTransaction error (here context.DeadlineExceeded, which
// looks the same as the node accepting the transaction and the acknowledgement
// being lost) is not treated as a definite failure. The record persists as
// TxBroadcastUnknown, not TxFailed, with the signed bytes, and a retry with the
// same key returns that record instead of signing and broadcasting again.
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

	// a same-key retry must not sign or broadcast again while the outcome is
	// unknown: unlike TxFailed, TxBroadcastUnknown is not retryable via Submit, and
	// Submit returns the existing record
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

// TestTxManagerRefreshStatusesResolvesBroadcastUnknownBySameBytes checks the
// other half of the ambiguous-broadcast behavior: once the transport recovers,
// RefreshStatuses resolves a TxBroadcastUnknown record by rebroadcasting the
// persisted bytes (same hash, same nonce), never by re-signing or allocating a
// new nonce, and advances it normally once a receipt appears.
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

	// the transport recovers and RefreshStatuses rebroadcasts the same bytes; no
	// receipt exists yet, so the record stays TxBroadcastUnknown
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

	// a receipt now appears and RefreshStatuses resolves the record normally
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

// TestTxManagerReplaceRejectsFeeCapExceededAfterBump checks that Replace
// rejects a replacement whose bumped fee exceeds the configured MaxFeePerGas,
// although buildTx's own check of the unbumped fee passes, and leaves the
// original transaction untouched and unbroadcast.
func TestTxManagerReplaceRejectsFeeCapExceededAfterBump(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	client.GasPrice = big.NewInt(1_000_000_000) // within the cap below, pre-bump
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManagerWithFeeCaps(client, txRepo, big.NewInt(31337), FeeModeLegacy, FeeCaps{
		MaxFeePerGas: big.NewInt(1_100_000_000), // a 20% bump pushes 1e9 to 1.2e9, past this cap
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

// TestTxManagerReplaceAmbiguousBroadcastRecoversViaRebroadcast checks that
// Replace persists the replacement record and marks the original Replaced
// before broadcasting. If the broadcast then fails ambiguously, the closest a
// single-process unit test gets to a crash right after the send, the
// replacement record stays durable (TxBroadcastUnknown, signed bytes persisted)
// and a later RefreshStatuses recovers it by rebroadcasting the same bytes.
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

	// the replacement and the original's Replaced link must already be durable
	// although the broadcast is unresolved
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
	// SentTxs[0] is the original transaction's broadcast from Submit;
	// SentTxs[1] must be exactly one rebroadcast of the replacement's persisted
	// bytes
	if len(client.SentTxs) != 2 {
		t.Fatalf("expected the original broadcast plus exactly 1 rebroadcast of the replacement, got %d sent", len(client.SentTxs))
	}
	last := client.SentTxs[len(client.SentTxs)-1]
	if last.Hash() != common.HexToHash(replacement.TxHash) || last.Nonce() != replacement.Nonce {
		t.Fatalf("rebroadcast must use the replacement's exact signed bytes/nonce, got hash=%s nonce=%d", last.Hash(), last.Nonce())
	}
}

// TestTxManagerReconcileReplacementsRepairsMissingLink checks that when the
// original's Replaced and ReplacedBy update never happened (a crash between
// persisting the replacement and linking the original), RefreshStatuses restores
// the link from the replacement's Replaces field.
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

	// simulate a replacement record that was persisted while its original was
	// never marked Replaced, the crash window reconcileReplacements repairs
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

	// RefreshStatuses needs a receipt for the orphan's placeholder hash, or
	// refreshOne would error while rebroadcasting undecodable bytes
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
// replacement record with Status TxFailed, which never reached the mempool,
// does not make reconcileReplacements mark a live, still-Pending original as
// Replaced.
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

	// a TxFailed replacement that still carries Replaces, as Replace's rejection
	// branch leaves it if its best-effort revert of the original to Pending were
	// lost, for example in a crash right after persisting newRecord as Failed
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
		t.Fatalf("original Status = %s, want still Pending: a TxFailed replacement must never mark it Replaced", stillPending.Status)
	}
	if stillPending.ReplacedBy != "" {
		t.Fatalf("original ReplacedBy = %q, want empty: a TxFailed replacement must never link itself", stillPending.ReplacedBy)
	}
}

// TestTxManagerRefreshStatusesConfirmedTxReorgsOnMissingReceipt checks that a
// Confirmed transaction whose receipt later disappears, the usual reorg symptom,
// moves to TxReorged.
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

	// the block, and this transaction's receipt, is reorged out
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

// TestTxManagerRefreshStatusesConfirmedTxReorgsOnBlockHashMismatch checks the
// other reorg signal: the receipt is still returned, but the canonical header
// at its height no longer has the hash the receipt recorded, because a
// different block became canonical at that height.
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
	// pin the pre-reorg canonical header so its hash is known, and record that
	// hash on the receipt, as a node would for a transaction mined in this block
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

	// a reorg replaces the canonical block at that height with a different one,
	// so the unchanged receipt now refers to a non-canonical block
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

// TestTxManagerSubmitPersistsBeforeBroadcast checks that a SendTransaction
// failure leaves a persisted record, and that the record is TxFailed
// (retryable), not TxPending, which would look like an in-flight transaction
// that was never sent.
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

// TestTxManagerSubmitRetriesAfterBroadcastFailure checks that a TxFailed
// record does not block its idempotency key: a later Submit with the same key
// resubmits and, on success, overwrites the same record ID.
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

// TestTxManagerSubmitRejectsFeeAboveCap checks that Submit rejects a fee above the configured cap
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

// TestTxManagerSubmitAllowsFeeWithinCap is the positive counterpart
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
	// AutoIncrementNonce simulates a node whose pending nonce reflects every
	// broadcast but unconfirmed transaction, including one from a concurrent
	// caller. Advancing the fake nonce by hand with SetNonce after Submit happens
	// after the per-key lock is released, which leaves a window where a second
	// Submit reads the stale nonce; that is a race in the test's simulation, not
	// in TxManager, and it can make this test flaky under -race
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

	// no receipt yet: stays pending
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ := txRepo.Get(context.Background(), rec.ID)
	if got.Status != models.TxPending {
		t.Fatalf("Status = %s, want pending", got.Status)
	}

	// the receipt appears at block 100 (the current block) with success but
	// without enough confirmations
	client.SetReceipt(txHash, &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(int64(client.BlockNum))})
	if err := mgr.RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ = txRepo.Get(context.Background(), rec.ID)
	if got.Status != models.TxMined {
		t.Fatalf("Status = %s, want mined", got.Status)
	}

	// the chain advances past the confirmations threshold
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
// floor(original*(100+bump)/100) can equal the original for small values, which
// the node rejects as underpriced. Rounding up plus the 1 wei guard must always
// exceed the original.
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

// TestTxManagerReplaceBumpsFromOriginalWhenFeesFell checks that when the
// current network suggestion has dropped below the original attempt's fee, the
// replacement still exceeds the original fee, instead of bumping the lower
// fresh quote, which the node would reject as underpriced.
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

	// network fees collapse before the replacement
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

// TestTxManagerAllocateNonceReconcilesAgainstLatest covers a lagging node that
// reports a pending nonce below the account's latest (mined) nonce. Submit must
// not sign below the latest nonce, which would fail with "nonce too low", so it
// uses the latest nonce instead of the pending one.
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

// TestTxManagerAllocateNonceReconcilesAgainstLocalRecords covers the opposite
// case: this manager's own record for a nonce that the node's pending view does
// not yet show is still respected, so a new Submit does not collide with it.
func TestTxManagerAllocateNonceReconcilesAgainstLocalRecords(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(priv.PublicKey)
	client := NewFakeClient()
	client.SetNonce(from, 0)
	client.SetMinedNonce(from, 0)
	txRepo := memory.NewTransactionRepository()
	mgr := NewTxManager(client, txRepo, big.NewInt(31337), FeeModeLegacy)
	to := common.HexToAddress("0x0000000000000000000000000000000000dEaD")

	// a locally tracked TxPending record at nonce 3 that the fake node's pending
	// and latest views do not know about
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

// TestTxManagerRefreshStatusesDetectsExternallyConsumedNonce checks that a
// TxPending record whose nonce the account's mined count has passed, with no
// receipt ever seen for its own hash, means another transaction was mined at
// that nonce. RefreshStatuses marks it TxNonceConsumedExternally instead of
// leaving it TxPending, and its idempotency key becomes retryable.
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

	// simulate another transaction (not TxManager's) being mined at this nonce:
	// the account's mined count advances past it, but no receipt exists for this
	// transaction's hash
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

	// with AllowReorgRetry unset, the default for a Kind whose on-chain
	// idempotence the manager cannot verify, the same idempotency key must not
	// retry, since an unknown replacement may have already performed the action,
	// and the signer is blocked, like an explicit TxNeedsIntervention record,
	// until an operator resolves it
	client.SetNonce(from, tx.Nonce+1)
	client.SetMinedNonce(from, tx.Nonce+1)
	if _, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "ext-consumed", PrivateKey: priv, To: to}); err == nil {
		t.Fatal("expected Submit to refuse a same-key retry after nonce-consumed-externally by default")
	}
	if _, err := mgr.Submit(context.Background(), SubmitRequest{IdempotencyKey: "unrelated-key", PrivateKey: priv, To: to}); err == nil {
		t.Fatal("expected Submit to block ANY submission for this signer, not just the same idempotency key, until resolved")
	}
}

// TestTxManagerSubmitAllowsReorgRetryWhenOptedIn is the opt-in counterpart: a
// Kind that sets AllowReorgRetry (for example compliance.StatusService's
// setStatus, which is safe to repeat) may resubmit under the same idempotency
// key after TxNonceConsumedExternally or TxReorged, and does not block the
// signer.
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

// TestTxManagerReplaceExhaustsAttemptsMarksNeedsIntervention checks that once
// a transaction's replacement attempts reach the cap, Replace signs and
// broadcasts nothing further for it and marks it TxNeedsIntervention.
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

	// the first replacement is within the cap (maxReplacementAttempts=1) and succeeds
	replacement, err := mgr.Replace(context.Background(), original.ID, NewStaticKeySigner(priv), 20)
	if err != nil {
		t.Fatalf("first replacement should succeed: %v", err)
	}
	sentBeforeSecond := len(client.SentTxs)

	// the second replacement targets the replacement record itself, which is
	// TxPending with ReplacementCount 1, over the cap of 1
	if _, err := mgr.Replace(context.Background(), replacement.ID, NewStaticKeySigner(priv), 20); err == nil {
		t.Fatal("expected an error once the replacement-attempt cap is exceeded")
	}
	if len(client.SentTxs) != sentBeforeSecond {
		t.Fatalf("expected no further broadcast once the cap is exceeded, sent %d, then %d", sentBeforeSecond, len(client.SentTxs))
	}

	stuck, err := txRepo.Get(context.Background(), replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stuck.Status != models.TxNeedsIntervention {
		t.Fatalf("Status = %s, want %s", stuck.Status, models.TxNeedsIntervention)
	}
}

// TestTxManagerSubmitBlockedBySignerNeedsIntervention checks that while any
// transaction for a signer is TxNeedsIntervention, Submit refuses new
// submissions for that signer, because a transaction behind a stuck nonce
// cannot be mined.
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

// TestTxManagerLeaseMutualExclusion covers the multi-replica case: two
// TxManager instances share one NonceLeaseRepository store, as two replicas
// sharing a Mongo deployment would, for the same signer. Replica A already holds
// the lease (taken directly against the shared store to stand in for a Submit in
// flight), so replica B's Submit fails with ErrLeaseUnavailable, with no nonce
// allocated, no record persisted and nothing broadcast.
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

// TestTxManagerLeaseAllowsSubmitOnceHeld checks the flip side: once a signer's
// lease is free, a "mongo-lease" TxManager acquires it and Submit proceeds,
// stamping the record with the acquired fencing token.
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

// TestNonceLeaseTakeoverAfterExpiryStrictlyIncrementsToken checks two
// properties of the lease store: another holder takes over an expired lease
// with a strictly greater fencing token, and Renew then rejects the superseded
// holder's stale (holder, token) pair.
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

	// the stale holder's guarded write is rejected: renewing with the old
	// (holder, token) pair fails now that replica-B holds the lease
	renewed, err := leases.Renew(context.Background(), key, "replica-A", tokenA, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed {
		t.Fatal("expected replica-A's renew to fail after replica-B's takeover")
	}

	// replica-B, the current holder, renews successfully
	renewed, err = leases.Renew(context.Background(), key, "replica-B", tokenB, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed {
		t.Fatal("expected replica-B's renew to succeed")
	}
}

// TestNonceLeaseReleaseFreesLeaseImmediately checks that Release lets another
// replica acquire at once, without waiting out the TTL, and that Release is
// fenced: a wrong holder or token is rejected.
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

// TestTxManagerInProcessModeUnaffectedByLeaseFeature checks that a plain
// TxManager, built without a lease store, needs no NonceLeaseRepository and
// works as in-process mode.
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

// versionRacingRepo wraps a repository.TransactionRepository. The first time
// Get is called for a given ID, it also bumps that record's Version through a
// no-op UpdateConditional before returning. This simulates another writer
// modifying the record between the caller's read and its conditional write,
// without goroutines: Replace's per-signer lock means that race could not
// happen within one txManager, and the test reproduces the cross-replica case
// the lock does not cover.
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

// TestTxManagerReplaceRejectsConcurrentlyModifiedOriginal checks that a stale
// writer is rejected at the repository write itself, not only by an earlier
// Renew. A writer whose read of the original is stale when it tries to mark it
// Replaced is rejected by UpdateConditional and does not broadcast its
// replacement.
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
		t.Fatalf("expected no broadcast when the conditional write loses the race, sent %d, then %d", sentBefore, len(client.SentTxs))
	}

	// the original is untouched by the losing attempt and stays as the injected
	// concurrent writer left it: Pending
	stillPending, err := backing.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.Status != models.TxPending {
		t.Fatalf("original Status = %s, want still Pending", stillPending.Status)
	}
}
