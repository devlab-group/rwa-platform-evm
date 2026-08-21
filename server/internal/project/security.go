package project

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

// ReconcileSecurity makes the DB Project record the live source of truth for
// on-chain governance authority by folding indexed
// chain events into models.Project.Security, instead of leaving those fields
// frozen at the deploy/verification snapshot. It projects the token's
// Paused/Unpaused, the SupplyController's AuditorChanged, the Vault's
// TreasuryChanged, the Strategy's price updates, every contract's
// AccessControl RoleGranted/RoleRevoked, and the two-step admin transfer's
// DefaultAdminTransferScheduled/Canceled into a single live SecurityState.
//
// Like the other read-model reconcilers (assets.ReconcileMinted/
// ReconcileAuditor), it is a FULL REPLAY over whatever chain_events currently
// holds, not an append-only delta feed: the indexer deletes reorged-out rows
// (indexer.Indexer.rollback), so re-deriving from scratch every tick is what
// makes an out-of-band pause/rotation/role change AND its later reorg both
// reflected correctly. The baseline it folds onto is the IMMUTABLE deploy
// config already on the Project record (Auditor/Treasury/prices and the
// configured role holders Admin/ComplianceOperator/Pricer/Treasurer/
// RedemptionManager) — verified to match the chain at deploy — never the
// previously-projected Security (which would make the baseline drift). Because
// the seed is the post-deploy state and grants/revokes are applied as set
// operations, folding is idempotent whether or not the indexer captured the
// deploy transaction's own construction events.
//
// Safe to call repeatedly and before any deployment: a non-Active project has
// no verified addresses to project onto, so it returns nil and the API keeps
// showing the deploy-config snapshot.
func ReconcileSecurity(ctx context.Context, projects repository.ProjectRepository, chainEvents repository.ChainEventRepository, checkpoints repository.IndexerCheckpointRepository, chainID int64) error {
	p, err := projects.Get(ctx)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil
		}
		return fmt.Errorf("project: load project for security reconcile: %w", err)
	}
	if p.Status != models.ProjectStatusActive || p.Addresses.Token == "" {
		return nil
	}

	state, err := foldSecurity(ctx, chainEvents, chainID, p)
	if err != nil {
		return err
	}

	// AsOfBlock/AsOfTime record how current the indexer's canonical view is:
	// the fold consumed every event chain_events holds, so the projection is
	// current up to the indexer checkpoint at this moment. The API turns the
	// gap between this and "now" into the securityStale signal.
	if cp, cerr := checkpoints.Get(ctx, chainID, indexer.CheckpointAddress); cerr == nil {
		state.AsOfBlock = cp.LastBlock
		state.AsOfTime = cp.UpdatedAt
	} else if cerr != repository.ErrNotFound {
		return fmt.Errorf("project: load indexer checkpoint for security reconcile: %w", cerr)
	}

	p.Security = state
	p.UpdatedAt = time.Now().UTC()
	if err := projects.Upsert(ctx, p); err != nil {
		return fmt.Errorf("project: persist security projection: %w", err)
	}
	return nil
}

// foldSecurity computes the live SecurityState from the deploy-config baseline
// on p plus every governance event for p's addresses.
func foldSecurity(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, p *models.Project) (*models.SecurityState, error) {
	a := p.Addresses
	state := &models.SecurityState{
		// Baselines from the immutable deploy config. A freshly deployed token
		// is unpaused, so the paused baseline is false.
		Auditor:                      p.Auditor,
		Treasury:                     p.Treasury,
		PurchasePricePerWholeToken:   p.PurchasePricePerWholeToken,
		RedemptionPricePerWholeToken: p.RedemptionPricePerWholeToken,
	}

	paused, err := latestPaused(ctx, chainEvents, chainID, a.Token)
	if err != nil {
		return nil, err
	}
	state.Paused = paused

	if a.SupplyController != "" {
		if v, ok, err := latestAddressField(ctx, chainEvents, chainID, a.SupplyController, "AuditorChanged", "newAuditor"); err != nil {
			return nil, err
		} else if ok {
			state.Auditor = v
		}
	}
	if a.Vault != "" {
		if v, ok, err := latestAddressField(ctx, chainEvents, chainID, a.Vault, "TreasuryChanged", "newTreasury"); err != nil {
			return nil, err
		} else if ok {
			state.Treasury = v
		}
	}
	// The Vault's strategy pointer is mutable, so fold it BEFORE reading
	// prices: after a setStrategy the prices that matter are the ones the
	// NEW strategy emits, and the old contract's last PurchasePriceUpdated
	// is not the live price any more. Everything downstream (the price
	// fold, the role fold, and the indexer address set rebuilt from
	// Security.Strategy) follows this address rather than the deploy
	// baseline.
	state.Strategy = a.Strategy
	if a.Vault != "" {
		if v, ok, err := latestAddressField(ctx, chainEvents, chainID, a.Vault, "StrategyChanged", "newStrategy"); err != nil {
			return nil, err
		} else if ok {
			state.Strategy = v
		}
	}
	if state.Strategy != "" {
		if v, ok, err := latestStringField(ctx, chainEvents, chainID, state.Strategy, "PurchasePriceUpdated", "newPrice"); err != nil {
			return nil, err
		} else if ok {
			state.PurchasePricePerWholeToken = v
		}
		if v, ok, err := latestStringField(ctx, chainEvents, chainID, state.Strategy, "RedemptionPriceUpdated", "newPrice"); err != nil {
			return nil, err
		} else if ok {
			state.RedemptionPricePerWholeToken = v
		}
	}

	perContract, err := foldRoles(ctx, chainEvents, chainID, p, state.Strategy)
	if err != nil {
		return nil, err
	}
	state.Roles = unionRoles(perContract)
	// Derived single-value role fields: prefer the still-holding configured
	// address (so the field is stable when nothing changed), else the
	// lexicographically-first current holder, else empty (role vacated).
	state.Admin = derive(p.Admin, unionHolders(perContract, "DEFAULT_ADMIN_ROLE", a.Token, a.Compliance, a.SupplyController, a.Vault, a.RedemptionEscrow, state.Strategy))
	state.ComplianceOperator = derive(p.ComplianceOperator, unionHolders(perContract, "COMPLIANCE_ROLE", a.Compliance))
	state.Pricer = derive(p.Pricer, unionHolders(perContract, "PRICER_ROLE", state.Strategy))
	state.Treasurer = derive(p.Treasurer, unionHolders(perContract, "TREASURER_ROLE", a.Vault, a.RedemptionEscrow))
	state.RedemptionManager = derive(p.RedemptionManager, unionHolders(perContract, "REDEMPTION_MANAGER_ROLE", a.RedemptionEscrow))

	// PendingAdmin: the incoming DEFAULT_ADMIN of an in-progress two-step admin
	// transfer (AccessControlDefaultAdminRules). Event-sourced and reorg-safe
	// like every field above — derived by full replay over the surviving
	// chain_events, never carried forward from a prior projection. The transfer
	// is begun and later accepted on ALL SIX governance contracts;
	// acceptDefaultAdminTransfer emits NO dedicated event but moves
	// DEFAULT_ADMIN_ROLE (RoleRevoked(old)+RoleGranted(new)), so "already
	// accepted on this contract" is detected by the scheduled newAdmin ALREADY
	// holding DEFAULT_ADMIN_ROLE there in the role fold (perContract). We report
	// the first still-pending per-contract value in canonical order, so the
	// field stays set for the whole handover and clears to "" only once the new
	// admin holds DEFAULT_ADMIN on every contract (or a
	// DefaultAdminTransferCanceled supersedes the schedule).
	for _, addr := range []string{a.Token, a.Compliance, a.SupplyController, a.Vault, a.RedemptionEscrow, a.Strategy} {
		if addr == "" {
			continue
		}
		adminHolders := perContract[strings.ToLower(addr)]["DEFAULT_ADMIN_ROLE"]
		pending, err := pendingAdminOnContract(ctx, chainEvents, chainID, addr, adminHolders)
		if err != nil {
			return nil, err
		}
		if pending != "" {
			state.PendingAdmin = pending
			break
		}
	}

	return state, nil
}

// pendingAdminOnContract returns the DEFAULT_ADMIN address a two-step transfer
// is currently handing addr to, or "" when none is pending on that contract.
// It compares the latest non-reorged DefaultAdminTransferScheduled against the
// latest DefaultAdminTransferCanceled by (block, logIndex): a Cancel that is
// not earlier than the Schedule supersedes it (nothing pending). There is no
// accept event — acceptDefaultAdminTransfer moves DEFAULT_ADMIN_ROLE via
// RoleRevoked(old)+RoleGranted(new) instead — so a scheduled newAdmin who
// ALREADY holds DEFAULT_ADMIN_ROLE here (per the role fold in adminHolders) has
// completed the accept on this contract and is no longer pending.
func pendingAdminOnContract(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, addr string, adminHolders map[string]bool) (string, error) {
	scheduled, err := latestEvent(ctx, chainEvents, chainID, addr, "DefaultAdminTransferScheduled")
	if err != nil {
		return "", err
	}
	if scheduled == nil {
		return "", nil
	}
	canceled, err := latestEvent(ctx, chainEvents, chainID, addr, "DefaultAdminTransferCanceled")
	if err != nil {
		return "", err
	}
	if canceled != nil && !earlier(canceled, scheduled) {
		return "", nil // a later cancel cleared the scheduled transfer
	}
	raw, _ := scheduled.Data["newAdmin"].(string)
	if raw == "" || !common.IsHexAddress(raw) {
		return "", fmt.Errorf("project: DefaultAdminTransferScheduled event %s/%d has no valid newAdmin", scheduled.TxHash, scheduled.LogIndex)
	}
	newAdmin := common.HexToAddress(raw).Hex()
	if adminHolders[newAdmin] {
		return "", nil // already accepted on this contract (role moved)
	}
	return newAdmin, nil
}

// foldRoles seeds each contract's per-role holder set from the deploy-config
// baseline (the same expected assignment verifyRoles proved matched the chain
// at deploy) and applies RoleGranted/RoleRevoked deltas in (block, logIndex)
// order. The result is keyed by lowercased contract address -> role name ->
// set of holder hex addresses.
// strategyAddr is the LIVE strategy (Security.Strategy), which after a
// Vault.setStrategy is no longer p.Addresses.Strategy — see foldSecurity.
func foldRoles(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, p *models.Project, strategyAddr string) (map[string]map[string]map[string]bool, error) {
	a := p.Addresses
	perContract := map[string]map[string]map[string]bool{}
	holderSet := func(addr, role string) map[string]bool {
		al := strings.ToLower(addr)
		if perContract[al] == nil {
			perContract[al] = map[string]map[string]bool{}
		}
		if perContract[al][role] == nil {
			perContract[al][role] = map[string]bool{}
		}
		return perContract[al][role]
	}
	seed := func(addr, role, holder string) {
		if addr == "" || holder == "" || !common.IsHexAddress(holder) {
			return
		}
		h := common.HexToAddress(holder)
		if h == (common.Address{}) {
			return
		}
		holderSet(addr, role)[h.Hex()] = true
	}

	// Deploy baseline — mirrors internal/project/verify.go verifyRoles's
	// expected map.
	contracts := []struct{ label, addr string }{
		{"token", a.Token}, {"compliance", a.Compliance}, {"supplyController", a.SupplyController},
		{"vault", a.Vault}, {"redemptionEscrow", a.RedemptionEscrow}, {"strategy", strategyAddr},
	}
	// A strategy swapped in after deployment carries no verified baseline —
	// verifyRoles never checked it — so seeding the deploy config's admin
	// and pricer onto it would assert authority nobody confirmed. Its
	// holders are exactly what its own role events say, and nothing else.
	swappedStrategy := strategyAddr != a.Strategy
	for _, c := range contracts {
		if c.label == "strategy" && swappedStrategy {
			continue
		}
		seed(c.addr, "DEFAULT_ADMIN_ROLE", p.Admin) // admin is DEFAULT_ADMIN on every child
	}
	seed(a.Token, "PAUSER_ROLE", p.Admin)
	seed(a.Compliance, "COMPLIANCE_ROLE", p.ComplianceOperator)
	if !swappedStrategy {
		seed(a.Strategy, "PRICER_ROLE", p.Pricer) // strategy only — the Vault gates nothing on it
	}
	seed(a.Vault, "TREASURER_ROLE", p.Treasurer)
	seed(a.RedemptionEscrow, "TREASURER_ROLE", p.Treasurer)
	seed(a.RedemptionEscrow, "REDEMPTION_MANAGER_ROLE", p.RedemptionManager)

	for _, c := range contracts {
		if c.addr == "" {
			continue
		}
		deltas, err := roleDeltas(ctx, chainEvents, chainID, c.addr)
		if err != nil {
			return nil, err
		}
		for _, d := range deltas {
			roleHex, _ := d.ev.Data["role"].(string)
			account, _ := d.ev.Data["account"].(string)
			if roleHex == "" || account == "" || !common.IsHexAddress(account) {
				continue
			}
			roleName, ok := bindings.RoleName(common.HexToHash(roleHex))
			if !ok {
				continue // a role outside the platform's model
			}
			holder := common.HexToAddress(account).Hex()
			if d.grant {
				holderSet(c.addr, roleName)[holder] = true
			} else if set := perContract[strings.ToLower(c.addr)][roleName]; set != nil {
				delete(set, holder)
			}
		}
	}
	return perContract, nil
}

type roleDelta struct {
	ev    *models.ChainEvent
	grant bool
}

// roleDeltas returns this contract's non-reorged RoleGranted/RoleRevoked
// events merged and sorted in (block, logIndex) order, so a grant-then-revoke
// (or revoke-then-grant) of the same account resolves to the final state.
func roleDeltas(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, addr string) ([]roleDelta, error) {
	granted, err := chainEvents.ListByName(ctx, chainID, addr, "RoleGranted")
	if err != nil {
		return nil, fmt.Errorf("project: list RoleGranted for %s: %w", addr, err)
	}
	revoked, err := chainEvents.ListByName(ctx, chainID, addr, "RoleRevoked")
	if err != nil {
		return nil, fmt.Errorf("project: list RoleRevoked for %s: %w", addr, err)
	}
	var deltas []roleDelta
	for _, e := range granted {
		if !e.Removed {
			deltas = append(deltas, roleDelta{e, true})
		}
	}
	for _, e := range revoked {
		if !e.Removed {
			deltas = append(deltas, roleDelta{e, false})
		}
	}
	sort.SliceStable(deltas, func(i, j int) bool {
		return earlier(deltas[i].ev, deltas[j].ev)
	})
	return deltas, nil
}

// unionRoles collapses the per-contract holder sets into the deduped, sorted
// role -> holders map the API exposes (a holder appears once per role no
// matter how many contracts grant it that role).
func unionRoles(perContract map[string]map[string]map[string]bool) map[string][]string {
	roles := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, byRole := range perContract {
		for role, holders := range byRole {
			for h := range holders {
				if seen[role] == nil {
					seen[role] = map[string]bool{}
				}
				if !seen[role][h] {
					seen[role][h] = true
					roles[role] = append(roles[role], h)
				}
			}
		}
	}
	for role := range roles {
		sort.Strings(roles[role])
	}
	if len(roles) == 0 {
		return nil
	}
	return roles
}

// unionHolders returns the sorted union of holders of role across addrs.
func unionHolders(perContract map[string]map[string]map[string]bool, role string, addrs ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, addr := range addrs {
		if addr == "" {
			continue
		}
		for h := range perContract[strings.ToLower(addr)][role] {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	sort.Strings(out)
	return out
}

// derive picks the live single-value role holder: the configured address if it
// still holds the role (keeping the field stable across no-op reconciles),
// otherwise the first current holder, otherwise "" (the role was vacated).
func derive(configured string, holders []string) string {
	if configured != "" && common.IsHexAddress(configured) {
		want := common.HexToAddress(configured).Hex()
		for _, h := range holders {
			if h == want {
				return want
			}
		}
	}
	if len(holders) > 0 {
		return holders[0]
	}
	return ""
}

// latestPaused returns the token's current paused flag: whichever of the most
// recent Paused / Unpaused events is later wins, with the deploy baseline
// (unpaused) when neither has ever fired.
func latestPaused(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	pausedEv, err := latestEvent(ctx, chainEvents, chainID, token, "Paused")
	if err != nil {
		return false, err
	}
	unpausedEv, err := latestEvent(ctx, chainEvents, chainID, token, "Unpaused")
	if err != nil {
		return false, err
	}
	switch {
	case pausedEv == nil:
		return false, nil
	case unpausedEv == nil:
		return true, nil
	default:
		return earlier(unpausedEv, pausedEv), nil // paused iff the Paused event is the later one
	}
}

// latestAddressField returns the address in field of the most recent
// (non-reorged) event named name at addr, and ok=false when no such event
// exists (leave the baseline in place). A malformed address in a surviving
// event is a decode/data error worth surfacing, matching assets.ReconcileAuditor.
func latestAddressField(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, addr, name, field string) (string, bool, error) {
	ev, err := latestEvent(ctx, chainEvents, chainID, addr, name)
	if err != nil {
		return "", false, err
	}
	if ev == nil {
		return "", false, nil
	}
	v, _ := ev.Data[field].(string)
	if v == "" || !common.IsHexAddress(v) {
		return "", false, fmt.Errorf("project: %s event %s/%d has no valid %s", name, ev.TxHash, ev.LogIndex, field)
	}
	return common.HexToAddress(v).Hex(), true, nil
}

// latestStringField returns the string in field of the most recent
// (non-reorged) event named name at addr — used for the strategy price
// events, whose values are already decimal strings.
func latestStringField(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, addr, name, field string) (string, bool, error) {
	ev, err := latestEvent(ctx, chainEvents, chainID, addr, name)
	if err != nil {
		return "", false, err
	}
	if ev == nil {
		return "", false, nil
	}
	v, _ := ev.Data[field].(string)
	if v == "" {
		return "", false, fmt.Errorf("project: %s event %s/%d has no %s", name, ev.TxHash, ev.LogIndex, field)
	}
	return v, true, nil
}

// latestEvent returns the most recent non-reorged event named name at addr, or
// nil when none survives. Ordering is by (block, logIndex) so a same-block
// pair resolves deterministically.
func latestEvent(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, addr, name string) (*models.ChainEvent, error) {
	events, err := chainEvents.ListByName(ctx, chainID, addr, name)
	if err != nil {
		return nil, fmt.Errorf("project: list %s for %s: %w", name, addr, err)
	}
	var latest *models.ChainEvent
	for _, e := range events {
		if e.Removed {
			continue
		}
		if latest == nil || earlier(latest, e) {
			latest = e
		}
	}
	return latest, nil
}

// earlier reports whether a precedes b in (block, logIndex) order.
func earlier(a, b *models.ChainEvent) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	return a.LogIndex < b.LogIndex
}
