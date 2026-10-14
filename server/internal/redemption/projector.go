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

// BuildStates replays RedemptionEscrow chain events in (blockNumber, logIndex)
// order and derives the current models.RedemptionRequest for each id, following
// the transition table in docs/spec/redemption-state-machine.md:
//
//	None -> Pending -> {Funded -> Completed | Rejected | Cancelled}
//
// Chain events are the only source of a request's status. An event without an
// id, or one that does not fit the request's current status (for example
// Funded before Requested), is ignored.
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
				r.FundedAt = e.IndexedAt.UTC()
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

// toInt64 converts the shapes an integer in ChainEvent.Data can take: the
// indexer's original Go type, int32 or int64 after a MongoDB round trip,
// float64 after JSON decoding, and decimal strings
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

// Reconcile rebuilds the redemption_requests read model from every persisted
// RedemptionEscrow chain event for (chainID, escrowAddr). Rebuilding from the
// events, not patching individual fields, lets the model recover when database
// records diverge. It is safe to call at any time, including after an indexer
// rollback that deleted events.
//
// It swaps generations as sales.Service.Reconcile does: each derived state is
// upserted under a fresh generation, and only then are records from older
// generations, such as requests whose events a reorg removed, deleted. A
// concurrent API reader never sees an empty or partial model.
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
