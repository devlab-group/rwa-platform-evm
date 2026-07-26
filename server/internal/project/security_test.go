package project

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

const secChainID = int64(31337)

func addr(hex string) string { return common.HexToAddress(hex).Hex() }

var (
	tokenAddr    = addr("0x0000000000000000000000000000000000000001")
	compAddr     = addr("0x0000000000000000000000000000000000000002")
	supplyAddr   = addr("0x0000000000000000000000000000000000000003")
	vaultAddr    = addr("0x0000000000000000000000000000000000000004")
	escrowAddr   = addr("0x0000000000000000000000000000000000000005")
	strategyAddr = addr("0x0000000000000000000000000000000000000006")

	adminA     = addr("0x00000000000000000000000000000000000000a1")
	compOp     = addr("0x00000000000000000000000000000000000000b2")
	pricer     = addr("0x00000000000000000000000000000000000000c3")
	treasurer  = addr("0x00000000000000000000000000000000000000d4")
	redemMgr   = addr("0x00000000000000000000000000000000000000e5")
	auditorA   = addr("0x00000000000000000000000000000000000000f6")
	treasuryA  = addr("0x0000000000000000000000000000000000000107")
	newAdminB  = addr("0x0000000000000000000000000000000000000208")
	newCompOpC = addr("0x0000000000000000000000000000000000000309")
)

func activeProject() *models.Project {
	return &models.Project{
		ProjectID: "p1", ChainID: secChainID, Status: models.ProjectStatusActive,
		Addresses: models.Addresses{
			Token: tokenAddr, Compliance: compAddr, SupplyController: supplyAddr,
			Vault: vaultAddr, RedemptionEscrow: escrowAddr, Strategy: strategyAddr,
			QuoteToken: addr("0x00000000000000000000000000000000000000ff"),
		},
		Admin: adminA, ComplianceOperator: compOp, Pricer: pricer, Treasurer: treasurer,
		RedemptionManager: redemMgr, Auditor: auditorA, Treasury: treasuryA,
		PurchasePricePerWholeToken: "1000", RedemptionPricePerWholeToken: "900",
	}
}

// setup seeds an Active project + an indexer checkpoint and returns the repos.
func setup(t *testing.T) *repository.Repositories {
	t.Helper()
	repos := memory.New()
	ctx := context.Background()
	if err := repos.Projects.Upsert(ctx, activeProject()); err != nil {
		t.Fatal(err)
	}
	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: secChainID, Address: indexer.CheckpointAddress, LastBlock: 100, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return repos
}

var evSeq int

func addEvent(t *testing.T, repos *repository.Repositories, address, name string, block uint64, logIndex uint, data map[string]any) {
	t.Helper()
	evSeq++
	e := &models.ChainEvent{
		ChainID: secChainID, Address: address, TxHash: fmt.Sprintf("0xtx%d", evSeq),
		LogIndex: logIndex, BlockNumber: block, Name: name, Data: data,
	}
	if err := repos.ChainEvents.Create(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func addRemovedEvent(t *testing.T, repos *repository.Repositories, address, name string, block uint64, logIndex uint, data map[string]any) {
	t.Helper()
	evSeq++
	e := &models.ChainEvent{
		ChainID: secChainID, Address: address, TxHash: fmt.Sprintf("0xtx%d", evSeq),
		LogIndex: logIndex, BlockNumber: block, Name: name, Data: data, Removed: true,
	}
	if err := repos.ChainEvents.Create(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func roleHex(id [32]byte) string { return common.Hash(id).Hex() }

func reconcile(t *testing.T, repos *repository.Repositories) *models.SecurityState {
	t.Helper()
	if err := ReconcileSecurity(context.Background(), repos.Projects, repos.ChainEvents, repos.IndexerCheckpoints, secChainID); err != nil {
		t.Fatalf("ReconcileSecurity: %v", err)
	}
	p, err := repos.Projects.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Security == nil {
		t.Fatal("Security projection is nil after reconcile")
	}
	return p.Security
}

// TestReconcileSecurityBaseline: with no governance events, the projection
// equals the immutable deploy-config baseline.
func TestReconcileSecurityBaseline(t *testing.T) {
	repos := setup(t)
	s := reconcile(t, repos)

	if s.Paused {
		t.Error("baseline should be unpaused")
	}
	if s.Auditor != auditorA || s.Treasury != treasuryA {
		t.Errorf("auditor/treasury = %s/%s, want %s/%s", s.Auditor, s.Treasury, auditorA, treasuryA)
	}
	if s.PurchasePricePerWholeToken != "1000" || s.RedemptionPricePerWholeToken != "900" {
		t.Errorf("prices = %s/%s, want 1000/900", s.PurchasePricePerWholeToken, s.RedemptionPricePerWholeToken)
	}
	if s.Admin != adminA || s.ComplianceOperator != compOp || s.Pricer != pricer || s.Treasurer != treasurer || s.RedemptionManager != redemMgr {
		t.Errorf("derived role fields = %+v", s)
	}
	if got := s.Roles["DEFAULT_ADMIN_ROLE"]; len(got) != 1 || got[0] != adminA {
		t.Errorf("DEFAULT_ADMIN_ROLE = %v, want [%s]", got, adminA)
	}
	if got := s.Roles["REDEMPTION_MANAGER_ROLE"]; len(got) != 1 || got[0] != redemMgr {
		t.Errorf("REDEMPTION_MANAGER_ROLE = %v, want [%s]", got, redemMgr)
	}
	if s.AsOfBlock != 100 {
		t.Errorf("AsOfBlock = %d, want 100", s.AsOfBlock)
	}
}

// TestReconcileSecurityPauseFlips: the latest of Paused/Unpaused wins.
func TestReconcileSecurityPauseFlips(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "Paused", 10, 0, map[string]any{"account": adminA})
	if !reconcile(t, repos).Paused {
		t.Fatal("expected paused after Paused event")
	}
	addEvent(t, repos, tokenAddr, "Unpaused", 11, 0, map[string]any{"account": adminA})
	if reconcile(t, repos).Paused {
		t.Fatal("expected unpaused after later Unpaused event")
	}
	// A Paused in an EARLIER block than the latest Unpaused must not win.
	addEvent(t, repos, tokenAddr, "Paused", 9, 0, map[string]any{"account": adminA})
	if reconcile(t, repos).Paused {
		t.Fatal("an earlier Paused must not override a later Unpaused")
	}
}

// TestReconcileSecurityAuditorTreasuryPrice: single-value authority fields
// track their latest events.
func TestReconcileSecurityAuditorTreasuryPrice(t *testing.T) {
	repos := setup(t)
	newAuditor := addr("0x0000000000000000000000000000000000000a11")
	newTreasury := addr("0x0000000000000000000000000000000000000a22")
	addEvent(t, repos, supplyAddr, "AuditorChanged", 10, 0, map[string]any{"newAuditor": newAuditor})
	addEvent(t, repos, vaultAddr, "TreasuryChanged", 10, 1, map[string]any{"newTreasury": newTreasury})
	addEvent(t, repos, strategyAddr, "PurchasePriceUpdated", 12, 0, map[string]any{"newPrice": "1500"})
	addEvent(t, repos, strategyAddr, "RedemptionPriceUpdated", 12, 1, map[string]any{"newPrice": "1400"})

	s := reconcile(t, repos)
	if s.Auditor != newAuditor {
		t.Errorf("auditor = %s, want %s", s.Auditor, newAuditor)
	}
	if s.Treasury != newTreasury {
		t.Errorf("treasury = %s, want %s", s.Treasury, newTreasury)
	}
	if s.PurchasePricePerWholeToken != "1500" || s.RedemptionPricePerWholeToken != "1400" {
		t.Errorf("prices = %s/%s, want 1500/1400", s.PurchasePricePerWholeToken, s.RedemptionPricePerWholeToken)
	}
}

// TestReconcileSecurityRoleDeltas: a grant adds a holder and a revoke removes
// one; the derived single field prefers the still-holding configured address.
func TestReconcileSecurityRoleDeltas(t *testing.T) {
	repos := setup(t)
	// Rotate the compliance operator: revoke the configured one, grant a new one.
	addEvent(t, repos, compAddr, "RoleRevoked", 10, 0, map[string]any{"role": roleHex(bindings.ComplianceRole), "account": compOp, "sender": adminA})
	addEvent(t, repos, compAddr, "RoleGranted", 10, 1, map[string]any{"role": roleHex(bindings.ComplianceRole), "account": newCompOpC, "sender": adminA})

	s := reconcile(t, repos)
	if got := s.Roles["COMPLIANCE_ROLE"]; len(got) != 1 || got[0] != newCompOpC {
		t.Errorf("COMPLIANCE_ROLE = %v, want [%s]", got, newCompOpC)
	}
	if s.ComplianceOperator != newCompOpC {
		t.Errorf("derived ComplianceOperator = %s, want %s", s.ComplianceOperator, newCompOpC)
	}
}

// TestReconcileSecurityAdminMultiContract: DEFAULT_ADMIN_ROLE is held on every
// contract, so revoking it on ONE contract must NOT drop the admin from the
// union — the per-contract fold is what makes this correct.
func TestReconcileSecurityAdminMultiContract(t *testing.T) {
	repos := setup(t)
	// A second admin granted on the vault only.
	addEvent(t, repos, vaultAddr, "RoleGranted", 10, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": newAdminB, "sender": adminA})
	// The original admin revoked on the vault only (still admin on 5 others).
	addEvent(t, repos, vaultAddr, "RoleRevoked", 11, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": adminA, "sender": adminA})

	s := reconcile(t, repos)
	got := s.Roles["DEFAULT_ADMIN_ROLE"]
	if len(got) != 2 {
		t.Fatalf("DEFAULT_ADMIN_ROLE = %v, want both admins (adminA still holds on 5 contracts)", got)
	}
	// derived Admin still prefers the configured adminA (holds elsewhere).
	if s.Admin != adminA {
		t.Errorf("derived Admin = %s, want configured %s (still holds on other contracts)", s.Admin, adminA)
	}
}

// TestReconcileSecurityGrantThenRevokeSameContract: order matters — a grant
// followed by a revoke of the same account leaves it absent.
func TestReconcileSecurityGrantThenRevokeSameContract(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, escrowAddr, "RoleGranted", 10, 0, map[string]any{"role": roleHex(bindings.RedemptionManagerRole), "account": newAdminB, "sender": adminA})
	addEvent(t, repos, escrowAddr, "RoleRevoked", 10, 1, map[string]any{"role": roleHex(bindings.RedemptionManagerRole), "account": newAdminB, "sender": adminA})

	s := reconcile(t, repos)
	if got := s.Roles["REDEMPTION_MANAGER_ROLE"]; len(got) != 1 || got[0] != redemMgr {
		t.Errorf("REDEMPTION_MANAGER_ROLE = %v, want only the baseline [%s]", got, redemMgr)
	}
}

// TestReconcileSecurityReorgRemovesEvent: a reorged-out (Removed) event is
// ignored, and full replay re-derives from the baseline — no stuck live state.
func TestReconcileSecurityReorgRemovesEvent(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "Paused", 10, 0, map[string]any{"account": adminA})
	if !reconcile(t, repos).Paused {
		t.Fatal("expected paused")
	}
	// Simulate the indexer rollback deleting the event, then re-reconcile.
	if _, err := repos.ChainEvents.DeleteFromBlock(context.Background(), secChainID, tokenAddr, 10); err != nil {
		t.Fatal(err)
	}
	if reconcile(t, repos).Paused {
		t.Fatal("paused must revert to baseline once the backing event is rolled back")
	}
}

// TestReconcileSecurityPendingAdminScheduled: a DefaultAdminTransferScheduled
// with no later accept/cancel surfaces the newAdmin as PendingAdmin.
func TestReconcileSecurityPendingAdminScheduled(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})

	s := reconcile(t, repos)
	if s.PendingAdmin != newAdminB {
		t.Fatalf("PendingAdmin = %q, want %s", s.PendingAdmin, newAdminB)
	}
}

// TestReconcileSecurityPendingAdminAccepted: once the scheduled newAdmin holds
// DEFAULT_ADMIN_ROLE on that contract (the accept moves the role, emitting no
// dedicated event), the transfer is no longer pending there.
func TestReconcileSecurityPendingAdminAccepted(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})
	if reconcile(t, repos).PendingAdmin != newAdminB {
		t.Fatal("expected PendingAdmin set after schedule")
	}
	// accept: DEFAULT_ADMIN_ROLE granted to newAdminB on the token at a later block.
	addEvent(t, repos, tokenAddr, "RoleGranted", 20, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": newAdminB, "sender": adminA})
	if s := reconcile(t, repos); s.PendingAdmin != "" {
		t.Fatalf("PendingAdmin = %q, want cleared after accept (role moved)", s.PendingAdmin)
	}
}

// TestReconcileSecurityPendingAdminCanceled: a later DefaultAdminTransferCanceled
// supersedes the schedule, clearing PendingAdmin.
func TestReconcileSecurityPendingAdminCanceled(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})
	if reconcile(t, repos).PendingAdmin != newAdminB {
		t.Fatal("expected PendingAdmin set after schedule")
	}
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferCanceled", 11, 0, map[string]any{})
	if s := reconcile(t, repos); s.PendingAdmin != "" {
		t.Fatalf("PendingAdmin = %q, want cleared after cancel", s.PendingAdmin)
	}
	// A cancel in an EARLIER block than the latest schedule must not clear it.
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 12, 0, map[string]any{"newAdmin": newAdminB})
	if s := reconcile(t, repos); s.PendingAdmin != newAdminB {
		t.Fatalf("PendingAdmin = %q, want %s (re-scheduled after the cancel)", s.PendingAdmin, newAdminB)
	}
}

// TestReconcileSecurityPendingAdminReorged: a reorged-out (Removed) schedule is
// ignored, so no transfer is reported pending.
func TestReconcileSecurityPendingAdminReorged(t *testing.T) {
	repos := setup(t)
	addRemovedEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})
	if s := reconcile(t, repos); s.PendingAdmin != "" {
		t.Fatalf("PendingAdmin = %q, want empty (schedule was reorged out)", s.PendingAdmin)
	}
}

// TestReconcileSecuritySkipsNonActive: an undeployed/non-Active project is not
// projected (no Security written).
func TestReconcileSecuritySkipsNonActive(t *testing.T) {
	repos := memory.New()
	ctx := context.Background()
	p := activeProject()
	p.Status = models.ProjectStatusDeploying
	p.Addresses.Token = ""
	if err := repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSecurity(ctx, repos.Projects, repos.ChainEvents, repos.IndexerCheckpoints, secChainID); err != nil {
		t.Fatalf("ReconcileSecurity on non-Active: %v", err)
	}
	got, _ := repos.Projects.Get(ctx)
	if got.Security != nil {
		t.Error("a non-Active project must not be projected")
	}
}
