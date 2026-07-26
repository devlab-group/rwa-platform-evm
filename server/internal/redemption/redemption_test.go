package redemption

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

func TestReconcileRebuildsReadModelFromEvents(t *testing.T) {
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000000E")
	events := memory.NewChainEventRepository()
	repo := memory.NewRedemptionRequestRepository()
	svc := New(nil, escrowAddr, repo, common.Address{})

	ctx := context.Background()
	mustCreate := func(name, id string, block uint64, extra map[string]any) {
		e := ev(name, id, block, 0, extra)
		e.Address = escrowAddr.Hex()
		e.ChainID = 31337
		if err := events.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate("RedemptionRequested", "1", 10, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)})
	mustCreate("RedemptionFunded", "1", 11, map[string]any{"funder": "0xBBB", "quoteAmount": "950"})

	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	r, err := svc.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status != "Funded" {
		t.Errorf("Status = %s, want Funded", r.Status)
	}

	// Reconcile again after a simulated rollback (event removed) must
	// reflect the new, smaller event set — proving it truly rebuilds
	// rather than only ever adding.
	if _, err := events.DeleteFromBlock(ctx, 31337, escrowAddr.Hex(), 11); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatal(err)
	}
	r, err = svc.Get(ctx, "1")
	if err != nil {
		t.Fatalf("Get after rollback reconcile: %v", err)
	}
	if r.Status != "Pending" {
		t.Errorf("Status after rollback = %s, want Pending", r.Status)
	}
}

func TestIsBeneficiaryAllowed(t *testing.T) {
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000000E")
	complianceAddr := common.HexToAddress("0x0000000000000000000000000000000000C0C0")
	client := blockchain.NewFakeClient()
	registry := bindings.NewComplianceRegistry()
	account := common.HexToAddress("0x0000000000000000000000000000000000B0B0")

	data, _ := registry.PackIsAllowed(account)
	ret, _ := registry.ABI.Methods["isAllowed"].Outputs.Pack(true)
	client.CallResponses[common.Bytes2Hex(data)] = ret

	svc := New(client, escrowAddr, memory.NewRedemptionRequestRepository(), complianceAddr)
	allowed, err := svc.IsBeneficiaryAllowed(context.Background(), account)
	if err != nil {
		t.Fatalf("IsBeneficiaryAllowed: %v", err)
	}
	if !allowed {
		t.Error("expected allowed=true")
	}
}

func TestIsBeneficiaryAllowedFalseWithoutComplianceConfigured(t *testing.T) {
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000000E")
	svc := New(nil, escrowAddr, memory.NewRedemptionRequestRepository(), common.Address{})
	allowed, err := svc.IsBeneficiaryAllowed(context.Background(), common.HexToAddress("0xB0B0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected allowed=false when compliance is not configured")
	}
}

func TestConfirmationsAndClaimable(t *testing.T) {
	pending := &models.RedemptionRequest{Status: models.RedemptionPending}
	if got := Confirmations(pending, 100); got != 0 {
		t.Errorf("Confirmations(pending) = %d, want 0", got)
	}
	if Claimable(pending, 100, 3) {
		t.Error("expected pending request to never be claimable")
	}

	funded := &models.RedemptionRequest{Status: models.RedemptionFunded, FundedAtBlock: 90}
	if got := Confirmations(funded, 95); got != 5 {
		t.Errorf("Confirmations(funded) = %d, want 5", got)
	}
	if Claimable(funded, 95, 10) {
		t.Error("expected not claimable below finality threshold")
	}
	if !Claimable(funded, 100, 10) {
		t.Error("expected claimable at/above finality threshold")
	}

	completed := &models.RedemptionRequest{Status: models.RedemptionCompleted, FundedAtBlock: 90}
	if Claimable(completed, 1000, 1) {
		t.Error("expected Completed request to never report claimable (already claimed)")
	}
}
