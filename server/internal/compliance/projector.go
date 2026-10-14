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
// (IComplianceRegistry.ComplianceStatus) to the domain string enum
var statusNames = map[uint8]models.ComplianceStatus{
	0: models.ComplianceUnknown,
	1: models.ComplianceAllowed,
	2: models.ComplianceBlocked,
}

// BuildStatuses replays ComplianceRegistry StatusChanged events in
// (blockNumber, logIndex) order and returns each account's current on-chain
// status and validUntil. The events say nothing about OwnershipVerified, which
// Reconcile keeps from the existing investor record.
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

// toUint8 and toInt64 accept the numeric types a ChainEvent.Data value can
// have. It keeps its original Go type (uint8, uint64, ...) straight from the
// indexer, but after a round trip through MongoDB the driver returns int32 or
// int64, whatever width was written.
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

// Reconcile rebuilds each account's Status and ValidUntil from the indexed
// ComplianceRegistry StatusChanged events, so a compliance action taken outside
// this server, such as by a multisig holding COMPLIANCE_ROLE, is reflected. The
// chain is the source of truth. OwnershipVerified is off-chain data, so it is
// kept from the existing investor record and defaults to false for an unknown
// account.
//
// Each call rebuilds from all persisted StatusChanged events. An account whose
// last such event was removed by an indexer reorg rollback (internal/indexer)
// has no state and is reset to Unknown with ValidUntil 0, so a rolled-back
// action cannot leave a wallet Allowed or Blocked.
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
