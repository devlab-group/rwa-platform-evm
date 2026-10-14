package project

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

// ReconcileSecurity rebuilds models.Project.Security from indexed events:
// pause, auditor and treasury rotation, strategy and price changes, freeze and
// forced transfer, role grants and revokes, and admin-transfer scheduling or
// cancellation.
//
// Each run starts from the verified deploy configuration and replays surviving
// chain_events, never the previous projection. Indexer rollback removes fork
// events, so replay reflects both wallet actions and their reorgs. Role updates
// are set operations, whether or not deploy-time role events were indexed.
//
// Repeated calls are safe. Before an Active deployment, return nil and leave
// the deploy-config snapshot visible.
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

	// AsOfBlock and AsOfTime record how current the projection is: the fold used
	// every event in chain_events, so it is current up to the indexer checkpoint
	// read here. The API turns the gap between this and the present time into the
	// securityStale signal.
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
		// baselines from the deploy config; a new token is unpaused, so the
		// paused baseline is false
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
	// The Vault's strategy can change, so fold it before the prices. After a
	// setStrategy the live prices are the ones the new strategy emits, not the old
	// contract's last price update. The price fold, the role fold and the
	// indexer's address set (rebuilt from Security.Strategy in cmd/platform) all
	// follow this address instead of the deployed one.
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

	frozen, err := foldFrozenBalances(ctx, chainEvents, chainID, a.Token)
	if err != nil {
		return nil, err
	}
	state.FrozenBalances = frozen

	lastForced, err := latestForcedTransfer(ctx, chainEvents, chainID, a.Token)
	if err != nil {
		return nil, err
	}
	state.LastForcedTransfer = lastForced

	perContract, err := foldRoles(ctx, chainEvents, chainID, p, state.Strategy)
	if err != nil {
		return nil, err
	}
	state.Roles = unionRoles(perContract)
	// Single-value role fields: the configured address if it still holds the
	// role, so the field stays stable when nothing changed, else the first
	// current holder in sorted order, else empty (the role was vacated)
	state.Admin = derive(p.Admin, unionHolders(perContract, "DEFAULT_ADMIN_ROLE", a.Token, a.Compliance, a.SupplyController, a.Vault, a.RedemptionEscrow, state.Strategy))
	state.ComplianceOperator = derive(p.ComplianceOperator, unionHolders(perContract, "COMPLIANCE_ROLE", a.Compliance))
	state.Pricer = derive(p.Pricer, unionHolders(perContract, "PRICER_ROLE", state.Strategy))
	state.Treasurer = derive(p.Treasurer, unionHolders(perContract, "TREASURER_ROLE", a.Vault, a.RedemptionEscrow))
	state.RedemptionManager = derive(p.RedemptionManager, unionHolders(perContract, "REDEMPTION_MANAGER_ROLE", a.RedemptionEscrow))

	// PendingAdmin reports the first pending DEFAULT_ADMIN_ROLE transfer in the
	// contract order below. Transfers are scheduled and accepted on each governance
	// contract. Acceptance emits RoleRevoked and RoleGranted; the role fold treats
	// the scheduled newAdmin holding the role as completion on that contract.
	// Replay retains pending transfers on the remaining contracts until accepted
	// or superseded by DefaultAdminTransferCanceled.
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

// pendingAdminOnContract returns the address that a two-step admin transfer is
// handing addr's DEFAULT_ADMIN_ROLE to, or "" when none is pending on that
// contract. It compares the latest surviving DefaultAdminTransferScheduled with
// the latest DefaultAdminTransferCanceled by (block, logIndex), and a cancel
// that is not earlier than the schedule leaves nothing pending. Accepting a
// transfer emits no event of its own, only RoleRevoked and RoleGranted, so a
// scheduled newAdmin that already holds DEFAULT_ADMIN_ROLE in adminHolders has
// accepted on this contract and is no longer pending.
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

// foldRoles seeds each contract's role holder sets from the deploy config (the
// assignment verifyRoles matched to the chain at adoption) and applies
// RoleGranted and RoleRevoked events in (block, logIndex) order. The result maps
// lowercased contract address to role name to the set of holder addresses.
// strategyAddr is the live strategy (Security.Strategy), which differs from
// p.Addresses.Strategy after a Vault.setStrategy; see foldSecurity.
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

	// deploy baseline, the same assignment as the expected map in verifyRoles
	contracts := []struct{ label, addr string }{
		{"token", a.Token}, {"compliance", a.Compliance}, {"supplyController", a.SupplyController},
		{"vault", a.Vault}, {"redemptionEscrow", a.RedemptionEscrow}, {"strategy", strategyAddr},
	}
	// A strategy swapped in after deployment has no verified baseline, because
	// verifyRoles never checked it, so seeding the deploy config's admin and
	// pricer onto it would assert authority nobody confirmed. Its holders are
	// exactly what its own role events show.
	swappedStrategy := strategyAddr != a.Strategy
	for _, c := range contracts {
		if c.label == "strategy" && swappedStrategy {
			continue
		}
		seed(c.addr, "DEFAULT_ADMIN_ROLE", p.Admin) // the admin holds DEFAULT_ADMIN_ROLE on every contract
	}
	seed(a.Token, "PAUSER_ROLE", p.Admin)
	seed(a.Compliance, "COMPLIANCE_ROLE", p.ComplianceOperator)
	if !swappedStrategy {
		seed(a.Strategy, "PRICER_ROLE", p.Pricer) // strategy only; the Vault has no pricer role
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

// foldFrozenBalances replays the token's surviving Frozen events in (block,
// logIndex) order into a map from holder to frozen amount. Like foldRoles, it
// replays the contract's full event history on every run. Each event carries an
// absolute amount, so a later event replaces an earlier one, and an amount of
// zero deletes the entry, which keeps the map the size of the currently frozen
// holders instead of the event count. A forced transfer that reduces a frozen
// amount emits its own Frozen event first, so the reduction is folded like any
// other.
func foldFrozenBalances(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, token string) (map[string]string, error) {
	if token == "" {
		return nil, nil
	}
	events, err := chainEvents.ListByName(ctx, chainID, token, "Frozen")
	if err != nil {
		return nil, fmt.Errorf("project: list Frozen for %s: %w", token, err)
	}
	surviving := make([]*models.ChainEvent, 0, len(events))
	for _, e := range events {
		if !e.Removed {
			surviving = append(surviving, e)
		}
	}
	sort.SliceStable(surviving, func(i, j int) bool { return earlier(surviving[i], surviving[j]) })

	balances := map[string]string{}
	for _, e := range surviving {
		account, _ := e.Data["account"].(string)
		amount, _ := e.Data["amount"].(string)
		// The decoder rejects a malformed Frozen log, so an unusable event here
		// means something upstream is broken. Log it and skip it: failing the fold
		// would leave the pause flag, roles, auditor and prices stale along with
		// the frozen map.
		if account == "" || !common.IsHexAddress(account) || amount == "" {
			log.Printf("project: skipping unusable Frozen event %s/%d (account=%q amount=%q)",
				e.TxHash, e.LogIndex, account, amount)
			continue
		}
		holder := common.HexToAddress(account).Hex()
		if isZeroAmount(amount) {
			delete(balances, holder)
			continue
		}
		balances[holder] = amount
	}
	if len(balances) == 0 {
		return nil, nil
	}
	return balances, nil
}

// isZeroAmount reports whether a base-10 amount string is zero, including
// strings with leading zeros
func isZeroAmount(amount string) bool {
	return strings.Trim(amount, "0") == ""
}

// latestForcedTransfer summarizes the most recent surviving ForcedTransfer on the
// token, so a reorg that removes the latest one falls back to the one before it.
// It returns nil when none survives.
//
// An unusable event is skipped in favor of the one before it, for the reason
// given on foldFrozenBalances: failing here would leave the pause flag, roles,
// auditor and prices stale, and operators rely on the pause flag during an
// incident.
func latestForcedTransfer(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, token string) (*models.ForcedTransferState, error) {
	if token == "" {
		return nil, nil
	}
	events, err := chainEvents.ListByName(ctx, chainID, token, "ForcedTransfer")
	if err != nil {
		return nil, fmt.Errorf("project: list ForcedTransfer for %s: %w", token, err)
	}
	surviving := make([]*models.ChainEvent, 0, len(events))
	for _, e := range events {
		if !e.Removed {
			surviving = append(surviving, e)
		}
	}
	sort.SliceStable(surviving, func(i, j int) bool { return earlier(surviving[j], surviving[i]) })

	for _, ev := range surviving {
		from, _ := ev.Data["from"].(string)
		to, _ := ev.Data["to"].(string)
		amount, _ := ev.Data["amount"].(string)
		if !common.IsHexAddress(from) || !common.IsHexAddress(to) || amount == "" {
			log.Printf("project: skipping unusable ForcedTransfer event %s/%d (from=%q to=%q amount=%q)",
				ev.TxHash, ev.LogIndex, from, to, amount)
			continue
		}
		return &models.ForcedTransferState{
			From:        common.HexToAddress(from).Hex(),
			To:          common.HexToAddress(to).Hex(),
			Amount:      amount,
			TxHash:      ev.TxHash,
			BlockNumber: ev.BlockNumber,
			LogIndex:    ev.LogIndex,
		}, nil
	}
	return nil, nil
}

type roleDelta struct {
	ev    *models.ChainEvent
	grant bool
}

// roleDeltas returns this contract's surviving RoleGranted and RoleRevoked
// events merged and sorted in (block, logIndex) order, so a grant followed by a
// revoke, or the reverse, of the same account resolves to the final state
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

// unionRoles merges the per-contract holder sets into the sorted role-to-holders
// map the API exposes, listing a holder once per role however many contracts
// grant it
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

// derive picks the single-value role holder: the configured address if it still
// holds the role, which keeps the field stable across reconciles that change
// nothing, otherwise the first current holder, otherwise "" (the role was
// vacated)
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

// latestPaused returns the token's paused flag: the later of the most recent
// Paused and Unpaused events wins, and the token is unpaused when neither has
// been emitted
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

// latestAddressField returns the address in field of the most recent surviving
// event named name at addr. ok is false when no such event exists, which leaves
// the baseline in place. A malformed address in a surviving event is returned as
// an error, as in assets.RecordService.ReconcileAuditor.
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

// latestStringField returns the string in field of the most recent surviving
// event named name at addr. It serves the strategy price events, whose values
// are decimal strings.
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

// latestEvent returns the most recent surviving event named name at addr, or nil
// when none survives. Events are ordered by (block, logIndex), so two events in
// one block resolve deterministically.
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
