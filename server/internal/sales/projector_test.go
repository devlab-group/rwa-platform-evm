package sales

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// probedPurchaseRepo reads between generation writes to detect empty or partial rebuild views
type probedPurchaseRepo struct {
	repository.PurchaseRepository
	probe func()
}

func (d probedPurchaseRepo) Upsert(ctx context.Context, p *models.Purchase) error {
	d.probe()
	return d.PurchaseRepository.Upsert(ctx, p)
}

func (d probedPurchaseRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	d.probe()
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
	got := purchases[0]
	if got.ID != "0xaa:0" || got.Buyer != "0xBUYER" || got.Recipient != "0xRECIP" || got.TokenAmount != "100" ||
		got.QuoteAmount != "95" || got.TxHash != "0xaa" || got.BlockNumber != 5 {
		t.Errorf("got %+v, want every field of the Purchased event mapped", got)
	}
}

func TestReconcileRebuildsPurchases(t *testing.T) {
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	events := memory.NewChainEventRepository()
	purchases := memory.NewPurchaseRepository()
	svc := New(nil, vaultAddr, common.Address{}, purchases)

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

	// Reconcile again after a simulated rollback must reflect the smaller set
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

// Replace all seeded purchases while another goroutine polls List; its count must not
// fall below the pre-rebuild value, detecting delete-first gaps
func TestReconcileNeverExposesEmptyOrPartialPurchasesBetweenWrites(t *testing.T) {
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	events := memory.NewChainEventRepository()
	inner := memory.NewPurchaseRepository()
	armed, probes, minObserved := false, 0, 1<<30
	purchases := probedPurchaseRepo{PurchaseRepository: inner, probe: func() {
		if !armed {
			return // only the rebuild under test is observed
		}
		probes++
		list, err := inner.List(context.Background())
		if err != nil {
			t.Errorf("probe List: %v", err)
			return
		}
		minObserved = min(minObserved, len(list))
	}}
	svc := New(nil, vaultAddr, common.Address{}, purchases)

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

	// reorg: every seeded tx is gone, replaced by three different ones
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

	armed = true
	if err := svc.Reconcile(ctx, events, 31337); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if probes == 0 {
		t.Fatal("the rebuild never wrote, so no intermediate state was observed")
	}
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
