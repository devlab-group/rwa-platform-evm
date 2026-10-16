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

// ReconcileSecurity rebuilds models.Project.Security from the verified deploy baseline
// and surviving chain_events, never the previous projection. It folds pause,
// auditor/treasury rotations, strategy/prices, freezes/forced transfers, roles and
// admin-transfer schedules/cancellations.
//
// Replay reflects indexer rollbacks; role set operations work with or without indexed
// deploy-time grants. Repeated calls are safe; missing or non-Active projects and
// projects without a token are skipped.
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

	// Stamp freshness from the checkpoint read after folding events; these reads are not
	// one snapshot. The API uses the age for securityStale.
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

// foldSecurity derives live SecurityState from p's deploy baseline and indexed events
// for its contracts
func foldSecurity(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, p *models.Project) (*models.SecurityState, error) {
	a := p.Addresses
	state := &models.SecurityState{
		// Deploy-config baselines; a newly deployed token is unpaused
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
	if p.Version == bindings.FactoryVersionV3 {
		state.MinPurchaseQuote = p.MinPurchaseQuote
		if a.Vault != "" {
			if v, ok, err := latestStringField(ctx, chainEvents, chainID, a.Vault, "MinPurchaseQuoteChanged", "newMinimum"); err != nil {
				return nil, err
			} else if ok {
				state.MinPurchaseQuote = v
			}
		}
	}
	// Resolve the live Vault strategy before folding prices and roles; cmd/platform also
	// uses Security.Strategy for the indexer watch set. Replacement-strategy price events
	// override deploy prices; without them, the deploy baseline remains.
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
	state.RolesByContract = rolesByContract(perContract, p.Addresses, state.Strategy)
	// derive selects stable single-value role fields from current holders
	state.Admin = derive(p.Admin, unionHolders(perContract, "DEFAULT_ADMIN_ROLE", a.Token, a.Compliance, a.SupplyController, a.Vault, a.RedemptionEscrow, state.Strategy, a.IncomeDistributor))
	state.ComplianceOperator = derive(p.ComplianceOperator, unionHolders(perContract, "COMPLIANCE_ROLE", a.Compliance))
	state.Pricer = derive(p.Pricer, unionHolders(perContract, "PRICER_ROLE", state.Strategy))
	state.Treasurer = derive(p.Treasurer, unionHolders(perContract, "TREASURER_ROLE", a.Vault, a.RedemptionEscrow))
	state.RedemptionManager = derive(p.RedemptionManager, unionHolders(perContract, "REDEMPTION_MANAGER_ROLE", a.RedemptionEscrow))

	// PendingAdmin selects the first pending transfer in the deployed-contract order
	// below. A scheduled newAdmin holding DEFAULT_ADMIN_ROLE counts as accepted; other
	// contracts remain pending until their transfers are accepted, cancelled or
	// superseded.
	for _, addr := range []string{a.Token, a.Compliance, a.SupplyController, a.Vault, a.RedemptionEscrow, a.Strategy, a.IncomeDistributor} {
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

// pendingAdminOnContract returns the scheduled newAdmin, or "" if none is pending. It
// compares the latest surviving schedule and cancellation by (block, logIndex);
// cancellation at or after the schedule clears it.
//
// Acceptance emits only RoleRevoked/RoleGranted, so newAdmin already holding
// DEFAULT_ADMIN_ROLE in adminHolders counts as accepted.
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

// foldRoles seeds verified deploy-time holders, then applies surviving
// RoleGranted/RoleRevoked events in (block, logIndex) order. It maps lowercase contract
// addresses to role names to holder sets.
//
// strategyAddr is the live Security.Strategy; a replacement strategy has no verified
// deploy baseline.
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

	// Same verified deploy baseline as verifyRoles
	contracts := []struct{ label, addr string }{
		{"token", a.Token}, {"compliance", a.Compliance}, {"supplyController", a.SupplyController},
		{"vault", a.Vault}, {"redemptionEscrow", a.RedemptionEscrow}, {"strategy", strategyAddr},
		{"incomeDistributor", a.IncomeDistributor},
	}
	// Do not seed deploy-time roles onto an unverified replacement strategy, nor onto a
	// deployed OraclePriceStrategy, which has no roles
	swappedStrategy := strategyAddr != a.Strategy || p.StrategyKind == models.StrategyKindOracle
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
	seed(a.IncomeDistributor, "INCOME_MANAGER_ROLE", p.IncomeManager)

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

// foldFrozenBalances replays the token's full surviving Frozen history in (block,
// logIndex) order. Absolute amounts replace earlier values; zero removes the holder,
// keeping map size bounded by currently frozen holders. Forced-transfer reductions emit
// Frozen first and follow the same fold.
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
		// Log and skip unusable stored events so one bad freeze does not stall pause, roles,
		// auditor or prices
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

// latestForcedTransfer returns the latest valid surviving token ForcedTransfer, or nil.
// Reorgs and unusable events fall back to earlier events; skipping bad data keeps the
// rest of Security updating, as in foldFrozenBalances.
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

// roleDeltas merges this contract's surviving RoleGranted/RoleRevoked events in (block,
// logIndex) order so later operations win
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

// unionRoles merges per-contract sets into the API's sorted role-to-holders map,
// deduplicating holders across contracts
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

// rolesByContract groups holders by contract label for UI role checks; omits contracts without
// holders
func rolesByContract(perContract map[string]map[string]map[string]bool, a models.Addresses, strategy string) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for label, addr := range map[string]string{
		"token": a.Token, "compliance": a.Compliance, "supplyController": a.SupplyController,
		"vault": a.Vault, "redemptionEscrow": a.RedemptionEscrow, "strategy": strategy,
		"incomeDistributor": a.IncomeDistributor,
	} {
		if addr == "" {
			continue
		}
		for role, holders := range perContract[strings.ToLower(addr)] {
			if len(holders) == 0 {
				continue
			}
			if out[label] == nil {
				out[label] = map[string][]string{}
			}
			for h := range holders {
				out[label][role] = append(out[label][role], h)
			}
			sort.Strings(out[label][role])
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unionHolders returns the sorted union of holders of role across addrs
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

// derive prefers the configured address if it still holds the role, otherwise the first
// sorted current holder, otherwise ""; this preserves unchanged defaults
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

// latestPaused uses the latest Paused/Unpaused event, defaulting to unpaused if neither
// exists
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

// latestAddressField reads field from the latest surviving name event at addr. No event
// returns ok=false, preserving the baseline; a malformed address returns an error, as
// in assets.RecordService.ReconcileAuditor.
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

// latestStringField reads the required non-empty field from the latest surviving name
// event at addr; strategy prices use decimal strings
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

// latestEvent returns the latest surviving name event at addr by (block, logIndex), or
// nil; log order breaks same-block ties
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

// earlier reports whether a precedes b in (block, logIndex) order
func earlier(a, b *models.ChainEvent) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	return a.LogIndex < b.LogIndex
}
