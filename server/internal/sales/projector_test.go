package sales

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// delayedPurchaseRepo wraps a repository.PurchaseRepository and inserts a
// small sleep around every write Reconcile's generation-swap performs,
// widening the window during which a concurrent reader's List() call can
// interleave with an in-flight rebuild — the concurrent
// API-reader test (TestReconcileNeverExposesEmptyOrPartialPurchasesToConcurrentReaders
// below) needs some real chance of that interleaving actually happening;
// against the bare memory repo's fast, individually-mutex-protected calls,
// an unmodified test would essentially never sample a mid-rebuild state
// even if the rebuild were still buggy.
type delayedPurchaseRepo struct {
	repository.PurchaseRepository
}

func (d delayedPurchaseRepo) Upsert(ctx context.Context, p *models.Purchase) error {
	time.Sleep(2 * time.Millisecond)
	return d.PurchaseRepository.Upsert(ctx, p)
}

func (d delayedPurchaseRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	time.Sleep(2 * time.Millisecond)
	return d.PurchaseRepository.DeleteStaleGeneration(ctx, gen)
}

func TestBuildPurchases(t *testing.T) {
	events := []*models.ChainEvent{
		{Name: "Purchased", TxHash: "0xaa", LogIndex: 0, BlockNumber: 5, Data: map[string]any{
			"buyer": "0xBUYER", "recipient": "0xRECIP", "tokenAmount": "100", "quoteAmount": "95",
		}},
		{Name: "ProceedsWithdrawn", TxHash: "0xcc", LogIndex: 0, BlockNumber: 7, Data: map[string]any{}},
	}

	purchases := BuildPurchases(events)
	if len(purchases) != 1 {
		t.Fatalf("expected 1 purchase, got %d", len(purchases))
	}
	if purchases[0].Buyer != "0xBUYER" || purchases[0].TokenAmount != "100" {
		t.Errorf("got %+v", purchases[0])
	}
}

func TestReconcileRebuildsPurchases(t *testing.T) {
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	events := memory.NewChainEventRepository()
	purchases := memory.NewPurchaseRepository()
	svc := New(nil, vaultAddr, common.Address{}, common.Address{}, purchases)

	ctx := context.Background()
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: vaultAddr.Hex(), Name: "Purchased", TxHash: "0xaa", LogIndex: 0, BlockNumber: 5,
		Data: map[string]any{"buyer": "0xBUYER", "recipient": "0xRECIP", "tokenAmount": "100", "quoteAmount": "95"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Reconcile(ctx, events, 31337); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	list, _, err := svc.ListPurchases(ctx, 10, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 purchase, got %d", len(list))
	}
	if list[0].Confirmations != 5 { // currentBlock 10 - blockNumber 5
		t.Errorf("Confirmations = %d, want 5", list[0].Confirmations)
	}

	// Reconcile again after a simulated rollback must reflect the smaller set.
	if _, err := events.DeleteFromBlock(ctx, 31337, vaultAddr.Hex(), 5); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx, events, 31337); err != nil {
		t.Fatal(err)
	}
	list, _, err = svc.ListPurchases(ctx, 10, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected purchases cleared after rollback, got %d", len(list))
	}
}

// TestReconcileNeverExposesEmptyOrPartialPurchasesToConcurrentReaders
// rebuilds the purchases read model
// TWICE, the second time replacing every previously-seeded purchase with
// entirely different ones (the worst case for a delete-then-reinsert
// rebuild — every old id disappears, every new id is new) while a
// concurrent goroutine polls List() in a tight loop. The old
// DeleteAll-then-Create implementation would let that reader observe an
// empty (or partially rebuilt) list; the generation-swap implementation
// must never let the observed count drop below the pre-rebuild count.
func TestReconcileNeverExposesEmptyOrPartialPurchasesToConcurrentReaders(t *testing.T) {
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	events := memory.NewChainEventRepository()
	purchases := delayedPurchaseRepo{memory.NewPurchaseRepository()}
	svc := New(nil, vaultAddr, common.Address{}, common.Address{}, purchases)

	ctx := context.Background()
	for i, tx := range []string{"0x1", "0x2", "0x3"} {
		if err := events.Create(ctx, &models.ChainEvent{
			ChainID: 31337, Address: vaultAddr.Hex(), Name: "Purchased", TxHash: tx, LogIndex: 0, BlockNumber: uint64(5 + i),
			Data: map[string]any{"buyer": "0xBUYER", "recipient": "0xRECIP", "tokenAmount": "100", "quoteAmount": "95"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Reconcile(ctx, events, 31337); err != nil {
		t.Fatalf("seed Reconcile: %v", err)
	}
	seeded, err := purchases.List(ctx)
	if err != nil || len(seeded) != 3 {
		t.Fatalf("seed: got %d purchases, err=%v", len(seeded), err)
	}

	// Reorg: every seeded tx is gone, replaced by three entirely
	// different ones.
	if _, err := events.DeleteFromBlock(ctx, 31337, vaultAddr.Hex(), 0); err != nil {
		t.Fatal(err)
	}
	for i, tx := range []string{"0x4", "0x5", "0x6"} {
		if err := events.Create(ctx, &models.ChainEvent{
			ChainID: 31337, Address: vaultAddr.Hex(), Name: "Purchased", TxHash: tx, LogIndex: 0, BlockNumber: uint64(20 + i),
			Data: map[string]any{"buyer": "0xBUYER2", "recipient": "0xRECIP2", "tokenAmount": "200", "quoteAmount": "190"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var minObserved int32 = 1 << 30
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			list, err := purchases.List(ctx)
			if err != nil {
				t.Errorf("concurrent List: %v", err)
				return
			}
			if len(list) == 0 {
				t.Error("concurrent reader observed an EMPTY purchases read model mid-rebuild")
				return
			}
			for {
				cur := atomic.LoadInt32(&minObserved)
				if int32(len(list)) >= cur || atomic.CompareAndSwapInt32(&minObserved, cur, int32(len(list))) {
					break
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()

	if err := svc.Reconcile(ctx, events, 31337); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	close(stop)
	wg.Wait()

	if minObserved < 3 {
		t.Errorf("concurrent reader observed as few as %d purchases mid-rebuild, want always >= 3 (the pre-rebuild count)", minObserved)
	}

	final, err := purchases.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 3 {
		t.Fatalf("final purchase count = %d, want 3", len(final))
	}
	for _, p := range final {
		if p.TxHash == "0x1" || p.TxHash == "0x2" || p.TxHash == "0x3" {
			t.Errorf("stale pre-reorg purchase %s survived the rebuild", p.TxHash)
		}
	}
}
