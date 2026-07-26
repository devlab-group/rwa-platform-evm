package compliance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// statusNames maps the on-chain uint8 ComplianceStatus encoding
// (IComplianceRegistry.ComplianceStatus) to the domain string enum.
var statusNames = map[uint8]models.ComplianceStatus{
	0: models.ComplianceUnknown,
	1: models.ComplianceAllowed,
	2: models.ComplianceBlocked,
}

// BuildStatuses replays ComplianceRegistry StatusChanged events in
// (blockNumber, logIndex) order and derives each account's current
// on-chain status. Chain state is the only source of truth for Status/
// ValidUntil; it says nothing about OwnershipVerified,
// which Reconcile preserves from the existing investor record.
func BuildStatuses(events []*models.ChainEvent) map[string]struct {
	Status     models.ComplianceStatus
	ValidUntil int64
} {
	sorted := make([]*models.ChainEvent, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BlockNumber != sorted[j].BlockNumber {
			return sorted[i].BlockNumber < sorted[j].BlockNumber
		}
		return sorted[i].LogIndex < sorted[j].LogIndex
	})

	states := map[string]struct {
		Status     models.ComplianceStatus
		ValidUntil int64
	}{}
	for _, e := range sorted {
		if e.Name != "StatusChanged" {
			continue
		}
		account := toString(e.Data["account"])
		if account == "" {
			continue
		}
		status, ok := statusNames[toUint8(e.Data["newStatus"])]
		if !ok {
			status = models.ComplianceUnknown
		}
		states[account] = struct {
			Status     models.ComplianceStatus
			ValidUntil int64
		}{Status: status, ValidUntil: toInt64(e.Data["newValidUntil"])}
	}
	return states
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// toUint8 and toInt64 accept every numeric shape a ChainEvent.Data value can
// actually arrive in: the original Go type (uint8/uint64/...) when it comes
// straight from the indexer within the same process, but int32 (small ints)
// or int64 (large/unsigned ints, via bson.Long) once it has round-tripped
// through MongoDB's Go driver, which decodes a stored BSON int32/int64 into
// exactly those two Go types regardless of the field's original width —
// never uint8, even for a value the indexer wrote as a Go uint8.
func toUint8(v any) uint8 {
	switch t := v.(type) {
	case uint8:
		return t
	case int32:
		return uint8(t)
	case int:
		return uint8(t)
	case int64:
		return uint8(t)
	case uint64:
		return uint8(t)
	case float64:
		return uint8(t)
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case uint64:
		return int64(t)
	case int64:
		return t
	case int32:
		return int64(t)
	case int:
		return int64(t)
	case float64:
		return int64(t)
	default:
		return 0
	}
}

// Reconcile rebuilds each account's Status/ValidUntil from indexed
// ComplianceRegistry StatusChanged events, so a manual/multisig compliance
// action taken outside this server (a compliance operator and/or legal
// multisig may share COMPLIANCE_ROLE) is
// reflected — chain is the source of truth, never the server's own
// optimistic upsert-on-submit alone. OwnershipVerified is preserved from
// whatever investor record already exists (or defaults to false for an
// account the server has never seen a challenge for); it is off-chain data
// the registry knows nothing about.
//
// The rebuild is exact and reversible: states is rebuilt from ALL currently
// persisted StatusChanged events on every call (not incrementally), so an
// account whose only/last such event was orphaned by an indexer reorg
// rollback (internal/indexer) simply won't appear in states anymore. Such
// accounts are reset here — Status/ValidUntil back to "no known status" —
// rather than left at whatever stale value a prior call upserted;
// otherwise a rolled-back compliance action would leave a wallet
// permanently (and incorrectly) Allowed or Blocked.
func Reconcile(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, complianceAddr string, investors repository.InvestorRepository) error {
	events, err := chainEvents.ListByName(ctx, chainID, complianceAddr, "StatusChanged")
	if err != nil {
		return fmt.Errorf("compliance: list StatusChanged events: %w", err)
	}
	states := BuildStatuses(events)

	now := time.Now().UTC()
	for account, s := range states {
		existing, err := investors.Get(ctx, account)
		ownershipVerified := false
		createdAt := now
		if err == nil {
			ownershipVerified = existing.OwnershipVerified
			createdAt = existing.CreatedAt
		} else if !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("compliance: load investor %s: %w", account, err)
		}
		if err := investors.Upsert(ctx, &models.Investor{
			Address: account, Status: s.Status, ValidUntil: s.ValidUntil,
			OwnershipVerified: ownershipVerified, CreatedAt: createdAt, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("compliance: upsert investor %s: %w", account, err)
		}
	}

	all, err := investors.List(ctx)
	if err != nil {
		return fmt.Errorf("compliance: list investors for reconcile: %w", err)
	}
	for _, inv := range all {
		if _, ok := states[inv.Address]; ok {
			continue // just upserted above from a surviving event
		}
		if inv.Status == models.ComplianceUnknown && inv.ValidUntil == 0 {
			continue // already at the no-event default; nothing to reset
		}
		// No surviving StatusChanged event backs this account's on-chain
		// status anymore. Status/ValidUntil are chain-derived, so reset
		// them; OwnershipVerified/CreatedAt are off-chain and preserved.
		inv.Status = models.ComplianceUnknown
		inv.ValidUntil = 0
		inv.UpdatedAt = now
		if err := investors.Upsert(ctx, inv); err != nil {
			return fmt.Errorf("compliance: reset investor %s with no surviving status event: %w", inv.Address, err)
		}
	}
	return nil
}
