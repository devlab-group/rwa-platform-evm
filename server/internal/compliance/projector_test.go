package compliance

import (
	"context"
	"testing"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

func ev(name, account string, block uint64, logIndex uint, extra map[string]any) *models.ChainEvent {
	data := map[string]any{"account": account}
	for k, v := range extra {
		data[k] = v
	}
	return &models.ChainEvent{Name: name, BlockNumber: block, LogIndex: logIndex, Data: data}
}

func TestBuildStatusesLatestEventWins(t *testing.T) {
	events := []*models.ChainEvent{
		ev("StatusChanged", "0xAAA", 10, 0, map[string]any{"newStatus": uint8(1), "newValidUntil": uint64(1000)}),
		ev("StatusChanged", "0xAAA", 20, 0, map[string]any{"newStatus": uint8(2), "newValidUntil": uint64(0)}),
	}
	states := BuildStatuses(events)
	s, ok := states["0xAAA"]
	if !ok {
		t.Fatal("expected state for 0xAAA")
	}
	if s.Status != models.ComplianceBlocked {
		t.Errorf("Status = %s, want Blocked (latest event)", s.Status)
	}
}

func TestBuildStatusesOrdersByBlockNotInputOrder(t *testing.T) {
	events := []*models.ChainEvent{
		ev("StatusChanged", "0xAAA", 20, 0, map[string]any{"newStatus": uint8(2), "newValidUntil": uint64(0)}),
		ev("StatusChanged", "0xAAA", 10, 0, map[string]any{"newStatus": uint8(1), "newValidUntil": uint64(1000)}),
	}
	states := BuildStatuses(events)
	if states["0xAAA"].Status != models.ComplianceBlocked {
		t.Errorf("Status = %s, want Blocked (block 20 is latest even though listed first)", states["0xAAA"].Status)
	}
}

func TestReconcilePreservesOwnershipVerified(t *testing.T) {
	ctx := context.Background()
	investors := memory.NewInvestorRepository()
	events := memory.NewChainEventRepository()
	complianceAddr := "0x0000000000000000000000000000000000C0A1"

	if err := investors.Upsert(ctx, &models.Investor{Address: "0xAAA", OwnershipVerified: true}); err != nil {
		t.Fatal(err)
	}
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: complianceAddr, Name: "StatusChanged", TxHash: "0x01", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"account": "0xAAA", "newStatus": uint8(1), "newValidUntil": uint64(0)},
	}); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, events, 31337, complianceAddr, investors); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated, err := investors.Get(ctx, "0xAAA")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.ComplianceAllowed {
		t.Errorf("Status = %s, want Allowed", updated.Status)
	}
	if !updated.OwnershipVerified {
		t.Error("expected OwnershipVerified to be preserved as true")
	}
}

func TestReconcileCreatesInvestorForOnChainOnlyAccount(t *testing.T) {
	ctx := context.Background()
	investors := memory.NewInvestorRepository()
	events := memory.NewChainEventRepository()
	complianceAddr := "0x0000000000000000000000000000000000C0A1"

	// No prior investor record — e.g. a manual compliance action for a
	// wallet that never went through the challenge flow.
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: complianceAddr, Name: "StatusChanged", TxHash: "0x01", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"account": "0xBBB", "newStatus": uint8(1), "newValidUntil": uint64(0)},
	}); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, events, 31337, complianceAddr, investors); err != nil {
		t.Fatal(err)
	}

	created, err := investors.Get(ctx, "0xBBB")
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != models.ComplianceAllowed {
		t.Errorf("Status = %s, want Allowed", created.Status)
	}
	if created.OwnershipVerified {
		t.Error("expected OwnershipVerified = false for a chain-only account")
	}
}

// TestReconcileResetsSubjectWhenLastStatusEventIsOrphaned covers
// reversibility: an investor's Status/ValidUntil must be
// reset to "no known status" once its only/last surviving StatusChanged
// event disappears from chainEvents — e.g. because internal/indexer rolled
// it back as orphaned by a reorg. The old reconciler only ever upserted
// accounts present in states and would leave this investor permanently,
// incorrectly stuck at its last chain-derived status.
func TestReconcileResetsSubjectWhenLastStatusEventIsOrphaned(t *testing.T) {
	ctx := context.Background()
	investors := memory.NewInvestorRepository()
	events := memory.NewChainEventRepository()
	complianceAddr := "0x0000000000000000000000000000000000C0A1"

	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: complianceAddr, Name: "StatusChanged", TxHash: "0x01", LogIndex: 0, BlockNumber: 10,
		Data: map[string]any{"account": "0xDDD", "newStatus": uint8(1), "newValidUntil": uint64(5000)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := investors.Upsert(ctx, &models.Investor{Address: "0xDDD", OwnershipVerified: true}); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, events, 31337, complianceAddr, investors); err != nil {
		t.Fatalf("Reconcile (apply): %v", err)
	}
	applied, err := investors.Get(ctx, "0xDDD")
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != models.ComplianceAllowed || applied.ValidUntil != 5000 {
		t.Fatalf("precondition failed: got status=%s validUntil=%d", applied.Status, applied.ValidUntil)
	}

	// Simulate the indexer rolling back the StatusChanged event as orphaned by a reorg.
	if _, err := events.DeleteFromBlock(ctx, 31337, complianceAddr, 10); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, events, 31337, complianceAddr, investors); err != nil {
		t.Fatalf("Reconcile (reset): %v", err)
	}
	reset, err := investors.Get(ctx, "0xDDD")
	if err != nil {
		t.Fatal(err)
	}
	if reset.Status != models.ComplianceUnknown {
		t.Errorf("Status = %s, want Unknown (reset after orphaned event was rolled back)", reset.Status)
	}
	if reset.ValidUntil != 0 {
		t.Errorf("ValidUntil = %d, want 0 after reset", reset.ValidUntil)
	}
	if !reset.OwnershipVerified {
		t.Error("expected OwnershipVerified to be preserved as true across the reset (off-chain data)")
	}
}

func TestReconcileReflectsManualStatusChangeOutsideServer(t *testing.T) {
	ctx := context.Background()
	investors := memory.NewInvestorRepository()
	events := memory.NewChainEventRepository()
	complianceAddr := "0x0000000000000000000000000000000000C0A1"

	// Server's own optimistic upsert said Allowed...
	if err := investors.Upsert(ctx, &models.Investor{Address: "0xCCC", Status: models.ComplianceAllowed}); err != nil {
		t.Fatal(err)
	}
	// ...but a multisig later blocked the wallet directly on-chain.
	if err := events.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: complianceAddr, Name: "StatusChanged", TxHash: "0x02", LogIndex: 0, BlockNumber: 20,
		Data: map[string]any{"account": "0xCCC", "newStatus": uint8(2), "newValidUntil": uint64(0)},
	}); err != nil {
		t.Fatal(err)
	}

	if err := Reconcile(ctx, events, 31337, complianceAddr, investors); err != nil {
		t.Fatal(err)
	}
	updated, err := investors.Get(ctx, "0xCCC")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.ComplianceBlocked {
		t.Errorf("Status = %s, want Blocked (chain must win over stale server-side state)", updated.Status)
	}
}
