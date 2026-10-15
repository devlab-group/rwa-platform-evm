package project

import (
	"context"
	"fmt"
	"strings"
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

// setup seeds an Active project and indexer checkpoint, returning the repositories
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

// Without governance events, Security matches the verified deploy baseline
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

// The latest Paused/Unpaused event wins
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
	// An earlier Paused must not override a later Unpaused
	addEvent(t, repos, tokenAddr, "Paused", 9, 0, map[string]any{"account": adminA})
	if reconcile(t, repos).Paused {
		t.Fatal("an earlier Paused must not override a later Unpaused")
	}
}

// Authority fields and prices follow their latest events
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

// Grants add holders, revokes remove them, and the single-value field prefers the
// configured address while it still holds the role
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

// Revoking DEFAULT_ADMIN_ROLE on one contract must not remove an admin who still holds
// it elsewhere from the union
func TestReconcileSecurityAdminMultiContract(t *testing.T) {
	repos := setup(t)
	// Second admin granted on the Vault only
	addEvent(t, repos, vaultAddr, "RoleGranted", 10, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": newAdminB, "sender": adminA})
	// Original admin revoked on the Vault; still admin on five contracts
	addEvent(t, repos, vaultAddr, "RoleRevoked", 11, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": adminA, "sender": adminA})

	s := reconcile(t, repos)
	got := s.Roles["DEFAULT_ADMIN_ROLE"]
	if len(got) != 2 {
		t.Fatalf("DEFAULT_ADMIN_ROLE = %v, want both admins (adminA still holds on 5 contracts)", got)
	}
	// Admin still prefers adminA, who holds the role elsewhere
	if s.Admin != adminA {
		t.Errorf("derived Admin = %s, want configured %s (still holds on other contracts)", s.Admin, adminA)
	}
}

// A grant followed by a revoke on the same contract leaves no holder
func TestReconcileSecurityGrantThenRevokeSameContract(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, escrowAddr, "RoleGranted", 10, 0, map[string]any{"role": roleHex(bindings.RedemptionManagerRole), "account": newAdminB, "sender": adminA})
	addEvent(t, repos, escrowAddr, "RoleRevoked", 10, 1, map[string]any{"role": roleHex(bindings.RedemptionManagerRole), "account": newAdminB, "sender": adminA})

	s := reconcile(t, repos)
	if got := s.Roles["REDEMPTION_MANAGER_ROLE"]; len(got) != 1 || got[0] != redemMgr {
		t.Errorf("REDEMPTION_MANAGER_ROLE = %v, want only the baseline [%s]", got, redemMgr)
	}
}

// Ignore Removed events and replay the baseline, leaving no stale state
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

// An uncancelled, unaccepted schedule exposes newAdmin as PendingAdmin
func TestReconcileSecurityPendingAdminScheduled(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})

	s := reconcile(t, repos)
	if s.PendingAdmin != newAdminB {
		t.Fatalf("PendingAdmin = %q, want %s", s.PendingAdmin, newAdminB)
	}
}

// Holding DEFAULT_ADMIN_ROLE on the scheduled contract signals acceptance without a
// dedicated event and clears that pending transfer
func TestReconcileSecurityPendingAdminAccepted(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})
	if reconcile(t, repos).PendingAdmin != newAdminB {
		t.Fatal("expected PendingAdmin set after schedule")
	}
	// Accept by granting DEFAULT_ADMIN_ROLE to newAdminB on the token at a later block
	addEvent(t, repos, tokenAddr, "RoleGranted", 20, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": newAdminB, "sender": adminA})
	if s := reconcile(t, repos); s.PendingAdmin != "" {
		t.Fatalf("PendingAdmin = %q, want cleared after accept (role moved)", s.PendingAdmin)
	}
}

// A later DefaultAdminTransferCanceled clears PendingAdmin
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
	// An earlier cancellation must not clear a later schedule
	addEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 12, 0, map[string]any{"newAdmin": newAdminB})
	if s := reconcile(t, repos); s.PendingAdmin != newAdminB {
		t.Fatalf("PendingAdmin = %q, want %s (re-scheduled after the cancel)", s.PendingAdmin, newAdminB)
	}
}

// A Removed schedule leaves no pending transfer
func TestReconcileSecurityPendingAdminReorged(t *testing.T) {
	repos := setup(t)
	addRemovedEvent(t, repos, tokenAddr, "DefaultAdminTransferScheduled", 10, 0, map[string]any{"newAdmin": newAdminB})
	if s := reconcile(t, repos); s.PendingAdmin != "" {
		t.Fatalf("PendingAdmin = %q, want empty (schedule was reorged out)", s.PendingAdmin)
	}
}

// Skip non-Active projects without writing Security
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

// After Vault.setStrategy, project the new address and its price events, ignoring the
// old strategy's updates
func TestReconcileSecurityStrategySwap(t *testing.T) {
	repos := setup(t)
	newStrategy := addr("0x0000000000000000000000000000000000000b11")

	// Old strategy prices before the swap; new strategy prices after it
	addEvent(t, repos, strategyAddr, "PurchasePriceUpdated", 10, 0, map[string]any{"newPrice": "1500"})
	addEvent(t, repos, strategyAddr, "RedemptionPriceUpdated", 10, 1, map[string]any{"newPrice": "1400"})
	addEvent(t, repos, vaultAddr, "StrategyChanged", 11, 0, map[string]any{"previousStrategy": strategyAddr, "newStrategy": newStrategy})
	addEvent(t, repos, newStrategy, "PurchasePriceUpdated", 12, 0, map[string]any{"newPrice": "2500"})
	addEvent(t, repos, newStrategy, "RedemptionPriceUpdated", 12, 1, map[string]any{"newPrice": "2400"})

	s := reconcile(t, repos)
	if s.Strategy != newStrategy {
		t.Errorf("Strategy = %s, want the Vault's current strategy %s", s.Strategy, newStrategy)
	}
	if s.PurchasePricePerWholeToken != "2500" || s.RedemptionPricePerWholeToken != "2400" {
		t.Errorf("prices = %s/%s, want the new strategy's 2500/2400", s.PurchasePricePerWholeToken, s.RedemptionPricePerWholeToken)
	}
}

// A replacement strategy inherits no verified deploy-time roles; its own events must
// establish PRICER_ROLE
func TestReconcileSecurityStrategySwapDropsUnverifiedBaseline(t *testing.T) {
	repos := setup(t)
	newStrategy := addr("0x0000000000000000000000000000000000000b11")
	addEvent(t, repos, vaultAddr, "StrategyChanged", 11, 0, map[string]any{"previousStrategy": strategyAddr, "newStrategy": newStrategy})

	s := reconcile(t, repos)
	if s.Pricer != "" {
		t.Errorf("Pricer = %s, want empty: the deploy baseline says nothing about the swapped-in strategy", s.Pricer)
	}
	for _, h := range s.Roles["PRICER_ROLE"] {
		if h == pricer {
			t.Errorf("PRICER_ROLE still lists the deploy-config pricer %s after a strategy swap: %v", pricer, s.Roles["PRICER_ROLE"])
		}
	}

	// Only a grant on the new strategy restores a pricer
	addEvent(t, repos, newStrategy, "RoleGranted", 12, 0, map[string]any{"role": roleHex(bindings.PricerRole), "account": pricer, "sender": adminA})
	s = reconcile(t, repos)
	if s.Pricer != pricer {
		t.Errorf("Pricer = %s after a grant on the new strategy, want %s", s.Pricer, pricer)
	}
}

// ERC-7943 enforcement projection

var (
	holderA = addr("0x0000000000000000000000000000000000000a01")
	holderB = addr("0x0000000000000000000000000000000000000b02")
)

func frozen(t *testing.T, repos *repository.Repositories, block uint64, logIndex uint, account, amount string) {
	t.Helper()
	addEvent(t, repos, tokenAddr, "Frozen", block, logIndex, map[string]any{"account": account, "amount": amount})
}

// Replay absolute overwrites, zero release and coexisting holders by (block, logIndex),
// regardless of insertion order
func TestFrozenBalancesFold(t *testing.T) {
	repos := setup(t)
	if s := reconcile(t, repos); s.FrozenBalances != nil {
		t.Errorf("baseline frozen balances = %v, want nil", s.FrozenBalances)
	}

	frozen(t, repos, 10, 0, holderA, "100")
	if got := reconcile(t, repos).FrozenBalances[holderA]; got != "100" {
		t.Errorf("first freeze = %q, want 100", got)
	}

	// Insert block 12 before 11; arrival-order folding would yield 250 instead of 400
	frozen(t, repos, 12, 0, holderA, "400")
	frozen(t, repos, 11, 0, holderA, "250")
	frozen(t, repos, 11, 1, holderB, "7")
	s := reconcile(t, repos)
	if s.FrozenBalances[holderA] != "400" {
		t.Errorf("holderA = %q, want 400 (absolute overwrite, latest wins)", s.FrozenBalances[holderA])
	}
	if s.FrozenBalances[holderB] != "7" {
		t.Errorf("holderB = %q, want 7", s.FrozenBalances[holderB])
	}

	// Zero removes the holder, bounding the map by currently frozen holders
	frozen(t, repos, 13, 0, holderA, "0")
	s = reconcile(t, repos)
	if _, ok := s.FrozenBalances[holderA]; ok {
		t.Errorf("released holder still present: %v", s.FrozenBalances)
	}
	if len(s.FrozenBalances) != 1 {
		t.Errorf("frozen balances = %v, want only holderB", s.FrozenBalances)
	}

	// Releasing the last holder omits the map
	frozen(t, repos, 14, 0, holderB, "0")
	if s := reconcile(t, repos); s.FrozenBalances != nil {
		t.Errorf("frozen balances = %v, want nil once nothing is frozen", s.FrozenBalances)
	}
}

// A uint256 far beyond int64/float64 must survive the projection verbatim.
func TestFrozenBalancesPreserveUint256(t *testing.T) {
	repos := setup(t)
	huge := "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	frozen(t, repos, 10, 0, holderA, huge)
	if got := reconcile(t, repos).FrozenBalances[holderA]; got != huge {
		t.Errorf("frozen amount = %q, want %q", got, huge)
	}
}

// Replay is idempotent and normalizes address keys to EIP-55
func TestFrozenBalancesIdempotentAndNormalized(t *testing.T) {
	repos := setup(t)
	frozen(t, repos, 10, 0, strings.ToLower(holderA), "100")

	first := reconcile(t, repos).FrozenBalances
	second := reconcile(t, repos).FrozenBalances
	if len(first) != 1 || first[holderA] != "100" {
		t.Fatalf("frozen balances = %v, want checksummed %s -> 100", first, holderA)
	}
	if len(second) != len(first) || second[holderA] != first[holderA] {
		t.Errorf("replay changed the projection: %v then %v", first, second)
	}
}

// Ignore a Removed freeze and restore the surviving history's state
func TestFrozenBalancesDropReorgedEvents(t *testing.T) {
	repos := setup(t)
	frozen(t, repos, 10, 0, holderA, "100")
	addRemovedEvent(t, repos, tokenAddr, "Frozen", 11, 0,
		map[string]any{"account": holderA, "amount": "500"})

	if got := reconcile(t, repos).FrozenBalances[holderA]; got != "100" {
		t.Errorf("holderA = %q, want 100 (the reorged 500 must not stick)", got)
	}

	// The same for a reorged-out release: the earlier freeze stands.
	addRemovedEvent(t, repos, tokenAddr, "Frozen", 12, 0,
		map[string]any{"account": holderA, "amount": "0"})
	if got := reconcile(t, repos).FrozenBalances[holderA]; got != "100" {
		t.Errorf("holderA = %q, want 100 after the release was reorged out", got)
	}
}

// Skip unreadable Frozen events so pause, roles, auditor and prices keep updating
func TestFrozenBalancesSkipMalformedEvent(t *testing.T) {
	repos := setup(t)
	addEvent(t, repos, tokenAddr, "Frozen", 10, 0, map[string]any{"account": holderA, "amount": "100"})
	addEvent(t, repos, tokenAddr, "Frozen", 11, 0, map[string]any{"account": "not-an-address", "amount": "1"})
	addEvent(t, repos, tokenAddr, "Frozen", 12, 0, map[string]any{"account": holderB, "amount": ""})

	s := reconcile(t, repos)
	if s.FrozenBalances[holderA] != "100" {
		t.Errorf("holderA = %q, want the readable event's 100", s.FrozenBalances[holderA])
	}
	if _, ok := s.FrozenBalances[holderB]; ok {
		t.Errorf("an amountless event should project nothing: %v", s.FrozenBalances)
	}
	if s.Auditor != auditorA {
		t.Errorf("the rest of the projection went stale: auditor = %s", s.Auditor)
	}
}

// Skip unreadable ForcedTransfer events, retaining the last readable summary and
// updating the rest of Security
func TestLastForcedTransferSkipsMalformedEvent(t *testing.T) {
	repos := setup(t)
	forcedTransfer(t, repos, 20, 0, holderA, holderB, "9")
	addEvent(t, repos, tokenAddr, "ForcedTransfer", 21, 0,
		map[string]any{"from": "not-an-address", "to": holderA, "amount": "5"})

	s := reconcile(t, repos)
	if s.LastForcedTransfer == nil || s.LastForcedTransfer.Amount != "9" {
		t.Fatalf("summary = %+v, want the readable block-20 seizure", s.LastForcedTransfer)
	}
	if s.Auditor != auditorA || s.Paused {
		t.Errorf("the rest of the projection went stale: auditor = %s paused = %v", s.Auditor, s.Paused)
	}

	// No readable event leaves the summary absent without failing
	repos2 := setup(t)
	addEvent(t, repos2, tokenAddr, "ForcedTransfer", 20, 0,
		map[string]any{"from": holderA, "to": holderB, "amount": ""})
	if s := reconcile(t, repos2); s.LastForcedTransfer != nil {
		t.Errorf("summary = %+v, want nil when no seizure is readable", s.LastForcedTransfer)
	}
}

func forcedTransfer(t *testing.T, repos *repository.Repositories, block uint64, logIndex uint, from, to, amount string) {
	t.Helper()
	addEvent(t, repos, tokenAddr, "ForcedTransfer", block, logIndex,
		map[string]any{"from": from, "to": to, "amount": amount})
}

// Use the latest (block, logIndex), regardless of insertion order, and retain its chain
// coordinates
func TestLastForcedTransferIsCanonicalLatest(t *testing.T) {
	repos := setup(t)
	if s := reconcile(t, repos); s.LastForcedTransfer != nil {
		t.Errorf("baseline last forced transfer = %+v, want nil", s.LastForcedTransfer)
	}

	forcedTransfer(t, repos, 21, 0, holderA, holderB, "5")
	forcedTransfer(t, repos, 20, 3, holderB, holderA, "9")
	s := reconcile(t, repos)
	if s.LastForcedTransfer == nil {
		t.Fatal("no forced-transfer summary projected")
	}
	if s.LastForcedTransfer.From != holderA || s.LastForcedTransfer.To != holderB || s.LastForcedTransfer.Amount != "5" {
		t.Errorf("summary = %+v, want the block-21 seizure", s.LastForcedTransfer)
	}
	if s.LastForcedTransfer.BlockNumber != 21 || s.LastForcedTransfer.TxHash == "" {
		t.Errorf("summary lacks chain coordinates: %+v", s.LastForcedTransfer)
	}
}

// Removing the latest seizure restores the previous one; removing all clears the
// summary
func TestLastForcedTransferReorg(t *testing.T) {
	repos := setup(t)
	forcedTransfer(t, repos, 20, 0, holderA, holderB, "9")
	addRemovedEvent(t, repos, tokenAddr, "ForcedTransfer", 21, 0,
		map[string]any{"from": holderB, "to": holderA, "amount": "5"})

	s := reconcile(t, repos)
	if s.LastForcedTransfer == nil || s.LastForcedTransfer.Amount != "9" {
		t.Fatalf("summary = %+v, want the surviving block-20 seizure", s.LastForcedTransfer)
	}

	repos2 := setup(t)
	addRemovedEvent(t, repos2, tokenAddr, "ForcedTransfer", 20, 0,
		map[string]any{"from": holderA, "to": holderB, "amount": "9"})
	if s := reconcile(t, repos2); s.LastForcedTransfer != nil {
		t.Errorf("summary = %+v, want nil once every seizure was reorged out", s.LastForcedTransfer)
	}
}

// Check distinct contract holders and their union for UI role checks
func TestReconcileSecurityRolesByContractDivergent(t *testing.T) {
	repos := setup(t)
	// Change admin and add a treasurer on the Vault only
	addEvent(t, repos, vaultAddr, "RoleGranted", 10, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": newAdminB, "sender": adminA})
	addEvent(t, repos, vaultAddr, "RoleRevoked", 10, 1, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": adminA, "sender": adminA})
	addEvent(t, repos, vaultAddr, "RoleGranted", 11, 0, map[string]any{"role": roleHex(bindings.TreasurerRole), "account": newCompOpC, "sender": adminA})

	s := reconcile(t, repos)
	by := s.RolesByContract
	if got := by["vault"]["DEFAULT_ADMIN_ROLE"]; len(got) != 1 || got[0] != newAdminB {
		t.Errorf("vault admins = %v, want [%s]", got, newAdminB)
	}
	for _, label := range []string{"token", "compliance", "supplyController", "redemptionEscrow", "strategy"} {
		if got := by[label]["DEFAULT_ADMIN_ROLE"]; len(got) != 1 || got[0] != adminA {
			t.Errorf("%s admins = %v, want [%s]", label, got, adminA)
		}
	}
	if got := by["vault"]["TREASURER_ROLE"]; len(got) != 2 {
		t.Errorf("vault treasurers = %v, want two", got)
	}
	if got := by["redemptionEscrow"]["TREASURER_ROLE"]; len(got) != 1 || got[0] != treasurer {
		t.Errorf("escrow treasurers = %v, want [%s]", got, treasurer)
	}
	if got := s.Roles["TREASURER_ROLE"]; len(got) != 2 {
		t.Errorf("union TREASURER_ROLE = %v, want two", got)
	}
	if got := s.Roles["DEFAULT_ADMIN_ROLE"]; len(got) != 2 {
		t.Errorf("union DEFAULT_ADMIN_ROLE = %v, want two", got)
	}
}
