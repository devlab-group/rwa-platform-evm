package redemption

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// BuildStates replays RedemptionEscrow chain events in (blockNumber,
// logIndex) order and derives the current models.RedemptionRequest for each
// id, following the FROZEN transition table in
// docs/spec/redemption-state-machine.md:
//
//	None -> Pending -> {Funded -> Completed | Rejected | Cancelled}
//
// This is the ONLY source of redemption status; it
// never reads server workflow tables. Events with an unrecognized id
// ordering (e.g. Funded before Requested, which cannot happen on a
// correctly indexed chain but could appear from a malformed test/fixture)
// are skipped rather than panicking.
func BuildStates(events []*models.ChainEvent, redemptionTimeout int64) map[string]*models.RedemptionRequest {
	sorted := make([]*models.ChainEvent, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BlockNumber != sorted[j].BlockNumber {
			return sorted[i].BlockNumber < sorted[j].BlockNumber
		}
		return sorted[i].LogIndex < sorted[j].LogIndex
	})

	states := map[string]*models.RedemptionRequest{}
	now := time.Now().UTC()

	for _, e := range sorted {
		id, _ := e.Data["id"].(string)
		if id == "" {
			continue
		}
		switch e.Name {
		case "RedemptionRequested":
			createdAt := toInt64(e.Data["createdAt"])
			states[id] = &models.RedemptionRequest{
				ID:            id,
				Beneficiary:   toString(e.Data["beneficiary"]),
				RWAAmount:     toString(e.Data["rwaAmount"]),
				QuoteAmount:   toString(e.Data["quoteAmount"]),
				Status:        models.RedemptionPending,
				CreatedAt:     createdAt,
				TimeoutAt:     createdAt + redemptionTimeout,
				RequestTxHash: e.TxHash,
				UpdatedAt:     now,
			}
		case "RedemptionFunded":
			if r, ok := states[id]; ok && r.Status == models.RedemptionPending {
				r.Status = models.RedemptionFunded
				r.FundTxHash = e.TxHash
				r.FundedAtBlock = e.BlockNumber
				r.UpdatedAt = now
			}
		case "RedemptionCompleted":
			if r, ok := states[id]; ok && r.Status == models.RedemptionFunded {
				r.Status = models.RedemptionCompleted
				r.ClaimTxHash = e.TxHash
				r.UpdatedAt = now
			}
		case "RedemptionRejected":
			if r, ok := states[id]; ok && r.Status == models.RedemptionPending {
				r.Status = models.RedemptionRejected
				r.ReasonCode = toString(e.Data["reasonCode"])
				r.UpdatedAt = now
			}
		case "RedemptionCancelled":
			if r, ok := states[id]; ok && r.Status == models.RedemptionPending {
				r.Status = models.RedemptionCancelled
				r.UpdatedAt = now
			}
		}
	}
	return states
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// toInt64 accepts every numeric shape a ChainEvent.Data value can arrive in
// (see internal/compliance/projector.go's toInt64 for why int32 is in this
// list: MongoDB's Go driver decodes any stored small int as int32, never
// the field's original Go width).
func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case uint64:
		return int64(t)
	case int32:
		return int64(t)
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

// Reconcile rebuilds the redemption_requests read model out of every
// persisted RedemptionEscrow chain event for (chainID, escrowAddr).
// Rebuilding fully from chain events (rather than incrementally patching
// individual fields) means the read model can always be reconstructed from
// chain events when database records diverge: it is always safe to
// call, including after an indexer reorg rollback that deleted some events
// out from under a previously-derived read model.
//
// This used to DeleteAll the whole collection before
// re-upserting every derived state, which let a concurrent API reader
// observe an empty read model, and left it that way if the process crashed
// or errored between the delete and the last upsert. It now does a
// generation-swap instead — see sales.Service.Reconcile's doc comment for
// the full reasoning, which applies identically here: every state is
// Upsert'd under the current generation first (never deleted), and only
// once that is complete are ids that no longer have a derived state at all
// (e.g. rolled back by a reorg) pruned by generation.
func (s *Service) Reconcile(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, redemptionTimeout int64) error {
	var all []*models.ChainEvent
	for _, name := range EventNames {
		evs, err := chainEvents.ListByName(ctx, chainID, s.escrowAddr.Hex(), name)
		if err != nil {
			return fmt.Errorf("redemption: list %s events: %w", name, err)
		}
		all = append(all, evs...)
	}

	states := BuildStates(all, redemptionTimeout)
	gen := time.Now().UnixNano()

	for _, r := range states {
		r.Generation = gen
		if err := s.repo.Upsert(ctx, r); err != nil {
			return fmt.Errorf("redemption: upsert %s: %w", r.ID, err)
		}
	}
	if err := s.repo.DeleteStaleGeneration(ctx, gen); err != nil {
		return fmt.Errorf("redemption: prune stale read-model entries: %w", err)
	}
	return nil
}
