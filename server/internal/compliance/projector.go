package compliance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// statusNames maps the on-chain uint8 ComplianceStatus encoding
// (IComplianceRegistry.ComplianceStatus) to the domain string enum
var statusNames = map[uint8]models.ComplianceStatus{
	0: models.ComplianceUnknown,
	1: models.ComplianceAllowed,
	2: models.ComplianceBlocked,
}

// BuildStatuses replays StatusChanged by (blockNumber, logIndex) into account
// status/validUntil. OwnershipVerified is off-chain and preserved separately by
// Reconcile.
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

// toUint8 and toInt64 accept indexer and MongoDB numeric types, including int32/int64.
// toUint8 maps values outside 0..255 to 0 (Unknown) to avoid wrapping into a valid status.
func toUint8(v any) uint8 {
	var n int64
	switch t := v.(type) {
	case uint8:
		return t
	case int32:
		n = int64(t)
	case int:
		n = int64(t)
	case int64:
		n = t
	case uint64:
		if t > math.MaxUint8 {
			return 0
		}
		n = int64(t)
	case float64:
		if t < 0 || t > math.MaxUint8 {
			return 0
		}
		n = int64(t)
	default:
		return 0
	}
	if n < 0 || n > math.MaxUint8 {
		return 0
	}
	return uint8(n)
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

// Reconcile rebuilds Status and ValidUntil from persisted ComplianceRegistry
// StatusChanged events, including multisig actions outside the server. It preserves
// off-chain OwnershipVerified, defaulting false for new accounts.
// When no event survives for an account, including after indexer reorg rollback, it
// resets Status to Unknown and ValidUntil to 0 so orphaned actions cannot leave stale
// Allowed/Blocked state.
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
		// No remaining StatusChanged event backs this account. Reset the
		// chain-derived fields and keep the off-chain ones.
		inv.Status = models.ComplianceUnknown
		inv.ValidUntil = 0
		inv.UpdatedAt = now
		if err := investors.Upsert(ctx, inv); err != nil {
			return fmt.Errorf("compliance: reset investor %s with no surviving status event: %w", inv.Address, err)
		}
	}
	return nil
}
