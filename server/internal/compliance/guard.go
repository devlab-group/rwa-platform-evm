package compliance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// ErrHolderCapReached signals that a new wallet would exceed the cap
var ErrHolderCapReached = errors.New("compliance: allowed-wallet cap reached")

// ErrSanctionedAddress signals a refused Allowed submission for a listed wallet
var ErrSanctionedAddress = errors.New("compliance: address is on the sanctions list")

// Alert kinds raised by guard refusals and checks
const (
	AlertHolderCapReached  = "holder_cap_reached"
	AlertHolderCapExceeded = "holder_cap_exceeded"
	AlertSanctionsMatch    = "sanctions_list_match"
)

// inFlightWindow releases stale transaction reservations; read-model Allowed wallets remain counted
const inFlightWindow = time.Hour

// AlertFunc raises an alert; address may be empty
type AlertFunc func(ctx context.Context, kind, address, detail string)

// Guard screens Allowed submissions for sanctions and caps newly counted wallets. Hold Lock across
// check, submit and operation recording. Lock is process-local; replicas need shared coordination.
type Guard struct {
	mu         sync.Mutex
	investors  repository.InvestorRepository
	ops        repository.ComplianceOperationRepository
	events     repository.KYCEventRepository
	txs        repository.TransactionRepository
	maxWallets int
	excluded   []string
	sanctions  *SanctionsList
}

// NewGuard configures cap and list checks. Exclusions apply to cap counting; maxWallets <= 0
// disables the cap, and nil sanctions matches nothing.
func NewGuard(repos *repository.Repositories, maxWallets int, excluded []string, sanctions *SanctionsList) *Guard {
	return &Guard{
		investors: repos.Investors, ops: repos.ComplianceOperations, events: repos.KYCEvents, txs: repos.Transactions,
		maxWallets: maxWallets, excluded: excluded, sanctions: sanctions,
	}
}

// Lock serializes check-and-submit sequences
func (g *Guard) Lock() { g.mu.Lock() }

// Unlock releases Lock
func (g *Guard) Unlock() { g.mu.Unlock() }

// Cap returns the configured cap, 0 meaning none
func (g *Guard) Cap() int { return g.maxWallets }

// ReloadSanctions reloads the sanctions file if it changed
func (g *Guard) ReloadSanctions() (bool, error) { return g.sanctions.Reload() }

// CheckAllow screens every address for sanctions; the cap applies only to newly counted,
// non-excluded wallets
func (g *Guard) CheckAllow(ctx context.Context, address string, now time.Time) error {
	if g.sanctions.Contains(address) {
		return ErrSanctionedAddress
	}
	if g.maxWallets <= 0 || g.isExcluded(address) {
		return nil
	}
	set, err := g.countedWallets(ctx, now)
	if err != nil {
		return err
	}
	if _, ok := set[strings.ToLower(address)]; ok {
		return nil
	}
	if len(set) >= g.maxWallets {
		return ErrHolderCapReached
	}
	return nil
}

// AllowedWallets lists unexpired Allowed wallets from the read model, excluding configured system
// addresses
func (g *Guard) AllowedWallets(ctx context.Context, now time.Time) ([]string, error) {
	all, err := g.investors.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, inv := range all {
		if inv.Status == models.ComplianceAllowed && (inv.ValidUntil == 0 || inv.ValidUntil >= now.Unix()) && !g.isExcluded(inv.Address) {
			out = append(out, inv.Address)
		}
	}
	return out, nil
}

// SanctionedAllowed filters AllowedWallets through the sanctions list
func (g *Guard) SanctionedAllowed(ctx context.Context, now time.Time) ([]string, error) {
	allowed, err := g.AllowedWallets(ctx, now)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range allowed {
		if g.sanctions.Contains(a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (g *Guard) isExcluded(address string) bool {
	for _, e := range g.excluded {
		if e != "" && strings.EqualFold(e, address) {
			return true
		}
	}
	return false
}

// countedWallets combines AllowedWallets with distinct addresses in recorded Allowed operations or
// Applying/Applied webhook events. A linked pending, mined, confirmed or broadcast_unknown
// transaction reserves a slot through inFlightWindow after SubmittedAt.
func (g *Guard) countedWallets(ctx context.Context, now time.Time) (map[string]struct{}, error) {
	allowed, err := g.AllowedWallets(ctx, now)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[strings.ToLower(a)] = struct{}{}
	}

	inFlight := func(address, txID string) error {
		if g.isExcluded(address) {
			return nil
		}
		if _, ok := set[strings.ToLower(address)]; ok {
			return nil
		}
		tx, err := g.txs.Get(ctx, txID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			return err
		}
		switch tx.Status {
		case models.TxPending, models.TxMined, models.TxConfirmed, models.TxBroadcastUnknown:
			if now.Sub(tx.SubmittedAt) <= inFlightWindow {
				set[strings.ToLower(address)] = struct{}{}
			}
		}
		return nil
	}

	// Both lists are fully scanned; use time-bounded queries if they grow large
	ops, err := g.ops.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		if op.Status == models.ComplianceAllowed {
			if err := inFlight(op.Address, op.ID); err != nil {
				return nil, err
			}
		}
	}
	events, err := g.events.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.Outcome == "Allowed" && e.TxID != "" && (e.ApplyStatus == models.KYCApplyApplying || e.ApplyStatus == models.KYCApplyApplied) {
			if err := inFlight(e.Address, e.TxID); err != nil {
				return nil, err
			}
		}
	}
	return set, nil
}

// refusalAlert maps a guard refusal to its alert kind, or "" for any other error
func refusalAlert(err error) string {
	switch {
	case errors.Is(err, ErrHolderCapReached):
		return AlertHolderCapReached
	case errors.Is(err, ErrSanctionedAddress):
		return AlertSanctionsMatch
	}
	return ""
}
