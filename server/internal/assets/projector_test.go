package assets

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

func TestReconcileMintedFlipsMatchingRecord(t *testing.T) {
	records := memory.NewAssetRecordRepository()
	events := memory.NewChainEventRepository()
	ctx := context.Background()

	rec := &models.AssetRecord{
		RecordID: "GOLD-1", Status: models.RecordStatusSigned, RecordKey: "0xrecordkey1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := records.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}

	controllerAddr := "0x0000000000000000000000000000000000C0DE"
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: controllerAddr, Name: "Minted", TxHash: "0xmint1", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"recordKey": "0xrecordkey1", "amount": "1000"},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &RecordService{}
	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatalf("ReconcileMinted: %v", err)
	}

	updated, err := records.Get(ctx, "GOLD-1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.RecordStatusMinted {
		t.Errorf("Status = %s, want Minted", updated.Status)
	}
	if updated.MintTxHash != "0xmint1" {
		t.Errorf("MintTxHash = %q, want 0xmint1", updated.MintTxHash)
	}
}

func TestReconcileMintedIgnoresNonMatchingRecords(t *testing.T) {
	records := memory.NewAssetRecordRepository()
	events := memory.NewChainEventRepository()
	ctx := context.Background()

	rec := &models.AssetRecord{RecordID: "GOLD-2", Status: models.RecordStatusPending, RecordKey: "0xother", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := records.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	controllerAddr := "0x0000000000000000000000000000000000C0DE"
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: controllerAddr, Name: "Minted", TxHash: "0xmint2", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"recordKey": "0xrecordkey-unrelated", "amount": "1000"},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &RecordService{}
	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatal(err)
	}
	updated, err := records.Get(ctx, "GOLD-2")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.RecordStatusPending {
		t.Errorf("Status = %s, want unchanged Pending", updated.Status)
	}
}

// TestReconcileMintedDemotesOrphanedMint checks reversibility: a record
// already flipped to Minted by a prior ReconcileMinted call must be demoted
// back to Signed (and MintTxHash cleared) if its backing Minted event later
// disappears from chainEvents — e.g. because internal/indexer rolled it back
// as orphaned by a reorg. A purely append-only reconciler would only ever
// promote records and leave this one permanently, incorrectly stuck at Minted.
func TestReconcileMintedDemotesOrphanedMint(t *testing.T) {
	records := memory.NewAssetRecordRepository()
	events := memory.NewChainEventRepository()
	ctx := context.Background()
	controllerAddr := "0x0000000000000000000000000000000000C0DE"

	rec := &models.AssetRecord{
		RecordID: "GOLD-4", Status: models.RecordStatusSigned, RecordKey: "0xrecordkey4",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := records.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: controllerAddr, Name: "Minted", TxHash: "0xmint4", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"recordKey": "0xrecordkey4", "amount": "1000"},
	}); err != nil {
		t.Fatal(err)
	}

	svc := &RecordService{}
	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatalf("ReconcileMinted (promote): %v", err)
	}
	promoted, err := records.Get(ctx, "GOLD-4")
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Status != models.RecordStatusMinted || promoted.MintTxHash != "0xmint4" {
		t.Fatalf("precondition failed: got status=%s mintTxHash=%s", promoted.Status, promoted.MintTxHash)
	}

	// Simulate the indexer rolling back the Minted event as orphaned by a reorg.
	if _, err := events.DeleteFromBlock(ctx, 31337, controllerAddr, 10); err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatalf("ReconcileMinted (demote): %v", err)
	}
	demoted, err := records.Get(ctx, "GOLD-4")
	if err != nil {
		t.Fatal(err)
	}
	if demoted.Status != models.RecordStatusSigned {
		t.Errorf("Status = %s, want Signed (demoted after orphaned Minted event was rolled back)", demoted.Status)
	}
	if demoted.MintTxHash != "" {
		t.Errorf("MintTxHash = %q, want cleared after demotion", demoted.MintTxHash)
	}
}

// TestReconcileMintedNeverDemotesRejected ensures the demotion pass only
// ever touches Minted records — Rejected is a terminal, non-chain-derived
// status this reconciler never set, and must never flip it back to Signed.
func TestReconcileMintedNeverDemotesRejected(t *testing.T) {
	records := memory.NewAssetRecordRepository()
	events := memory.NewChainEventRepository()
	ctx := context.Background()
	controllerAddr := "0x0000000000000000000000000000000000C0DE"

	rec := &models.AssetRecord{RecordID: "GOLD-5", Status: models.RecordStatusRejected, RecordKey: "0xrecordkey5", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := records.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}

	svc := &RecordService{}
	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatal(err)
	}
	unchanged, err := records.Get(ctx, "GOLD-5")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != models.RecordStatusRejected {
		t.Errorf("Status = %s, want unchanged Rejected", unchanged.Status)
	}
}

func TestReconcileMintedIsIdempotent(t *testing.T) {
	records := memory.NewAssetRecordRepository()
	events := memory.NewChainEventRepository()
	ctx := context.Background()
	rec := &models.AssetRecord{RecordID: "GOLD-3", Status: models.RecordStatusSigned, RecordKey: "0xrk3", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_ = records.Create(ctx, rec)
	controllerAddr := "0x0000000000000000000000000000000000C0DE"
	_ = events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: controllerAddr, Name: "Minted", TxHash: "0xmint3", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"recordKey": "0xrk3", "amount": "1000"},
	})

	svc := &RecordService{}
	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileMinted(ctx, events, 31337, controllerAddr, records); err != nil {
		t.Fatalf("second ReconcileMinted call: %v", err)
	}
	updated, err := records.Get(ctx, "GOLD-3")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.RecordStatusMinted {
		t.Errorf("Status = %s, want Minted", updated.Status)
	}
}

// TestReconcileAuditorRestoresBaselineWhenOnlyRotationIsRolledBack rolls back
// the first/only auditor rotation: once the sole surviving AuditorChanged
// event disappears (a deep reorg the indexer rolled back past), the live
// auditor must not stay stuck at the now-orphaned rotated-to address forever —
// ReconcileAuditor restores the deployment-time baseline instead.
func TestReconcileAuditorRestoresBaselineWhenOnlyRotationIsRolledBack(t *testing.T) {
	events := memory.NewChainEventRepository()
	ctx := context.Background()
	controllerAddr := "0x0000000000000000000000000000000000C0DE"
	baseline := common.HexToAddress("0x00000000000000000000000000000000000001")
	rotated := common.HexToAddress("0x00000000000000000000000000000000000002")

	svc := &RecordService{auditor: baseline, baselineAuditor: baseline}

	// The rotation happens on-chain.
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: controllerAddr, Name: "AuditorChanged", TxHash: "0xrotate1", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"newAuditor": rotated.Hex()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileAuditor(ctx, events, 31337, controllerAddr); err != nil {
		t.Fatalf("ReconcileAuditor (apply rotation): %v", err)
	}
	if svc.currentAuditor() != rotated {
		t.Fatalf("currentAuditor() = %s, want the rotated-to address %s", svc.currentAuditor().Hex(), rotated.Hex())
	}

	// A deep reorg rolls back past block 10 — the indexer deletes the
	// underlying chain_events row entirely (not merely Removed=true).
	if _, err := events.DeleteFromBlock(ctx, 31337, controllerAddr, 10); err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileAuditor(ctx, events, 31337, controllerAddr); err != nil {
		t.Fatalf("ReconcileAuditor (after rollback): %v", err)
	}
	if svc.currentAuditor() != baseline {
		t.Fatalf("currentAuditor() = %s, want restored baseline %s (not stuck at orphaned %s)", svc.currentAuditor().Hex(), baseline.Hex(), rotated.Hex())
	}
}
