package memory

import (
	"context"
	"testing"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

func TestChainEventRepositoryListAllIncludesRemovedSortedByPosition(t *testing.T) {
	repo := NewChainEventRepository()
	ctx := context.Background()
	// Two chains; ListAll must return only the requested chain's events,
	// including a Removed one, ordered by (block, logIndex).
	mk := func(chainID int64, addr, txHash string, block uint64, logIndex uint, removed bool) *models.ChainEvent {
		return &models.ChainEvent{ChainID: chainID, Address: addr, TxHash: txHash, BlockNumber: block, LogIndex: logIndex, Name: "Purchased", Removed: removed}
	}
	_ = repo.Create(ctx, mk(31337, "0xA", "0x1", 10, 1, false))
	_ = repo.Create(ctx, mk(31337, "0xA", "0x2", 10, 0, true)) // removed, earlier logIndex
	_ = repo.Create(ctx, mk(31337, "0xB", "0x3", 12, 0, false))
	_ = repo.Create(ctx, mk(999, "0xA", "0x9", 1, 0, false)) // other chain

	got, err := repo.ListAll(ctx, 31337)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("ListAll len = %d, want 3 (removed included, other chain excluded)", len(got))
	}
	// (10,0) removed, then (10,1), then (12,0).
	if got[0].TxHash != "0x2" || !got[0].Removed || got[1].TxHash != "0x1" || got[2].TxHash != "0x3" {
		t.Fatalf("order = [%s,%s,%s]", got[0].TxHash, got[1].TxHash, got[2].TxHash)
	}
}

func TestTransactionRepositoryGetByTxHash(t *testing.T) {
	repo := NewTransactionRepository()
	ctx := context.Background()

	if _, err := repo.GetByTxHash(ctx, 31337, "0xabc"); err != repository.ErrNotFound {
		t.Fatalf("empty repo err = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByTxHash(ctx, 31337, ""); err != repository.ErrNotFound {
		t.Fatalf("empty txHash err = %v, want ErrNotFound", err)
	}

	_ = repo.Create(ctx, &models.Transaction{ID: "mgr-1", ChainID: 31337, TxHash: "0xabc", Status: models.TxConfirmed})
	got, err := repo.GetByTxHash(ctx, 31337, "0xabc")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "mgr-1" {
		t.Fatalf("ID = %s, want mgr-1", got.ID)
	}
	// Wrong chain must not match.
	if _, err := repo.GetByTxHash(ctx, 1, "0xabc"); err != repository.ErrNotFound {
		t.Fatalf("wrong-chain err = %v, want ErrNotFound", err)
	}

	// When both a manager and an event-derived record share a hash, the
	// manager (non-EventDerived) record is returned so the projector dedups.
	_ = repo.Create(ctx, &models.Transaction{ID: models.EventDerivedTxIDPrefix + "0xdef", ChainID: 31337, TxHash: "0xdef", Status: models.TxConfirmed})
	_ = repo.Create(ctx, &models.Transaction{ID: "mgr-2", ChainID: 31337, TxHash: "0xdef", Status: models.TxConfirmed})
	got, err = repo.GetByTxHash(ctx, 31337, "0xdef")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "mgr-2" || models.IsEventDerived(got) {
		t.Fatalf("shared-hash returned %s (evt=%v), want the manager record mgr-2", got.ID, models.IsEventDerived(got))
	}
}
