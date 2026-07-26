package sales

import (
	"context"
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// BuildPurchases converts decoded Vault Purchased chain events into
// models.Purchase records, one per event (each Purchased log is a
// self-contained fill, unlike redemption's multi-event lifecycle).
func BuildPurchases(events []*models.ChainEvent) []*models.Purchase {
	out := make([]*models.Purchase, 0, len(events))
	for _, e := range events {
		if e.Name != "Purchased" {
			continue
		}
		out = append(out, &models.Purchase{
			ID:          e.TxHash + fmt.Sprintf(":%d", e.LogIndex),
			Buyer:       toString(e.Data["buyer"]),
			Recipient:   toString(e.Data["recipient"]),
			TokenAmount: toString(e.Data["tokenAmount"]),
			QuoteAmount: toString(e.Data["quoteAmount"]),
			TxHash:      e.TxHash, BlockNumber: e.BlockNumber, CreatedAt: e.IndexedAt,
		})
	}
	return out
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// Reconcile rebuilds the purchases read model out of every persisted Vault
// chain event for (chainID, vaultAddr), mirroring internal/redemption.
// Service.Reconcile: always safe to call, including after an indexer
// reorg rollback.
//
// This used to DeleteAll then Create every record, which let
// a concurrent API reader observe an empty or partially rebuilt history —
// and a crash between the delete and the last insert left it that way
// until the next successful pass. It now does a generation-swap instead:
// every surviving record is Upsert'd (create-or-replace by id, never
// deleted first) tagged with a fresh generation, so a reader mid-rebuild
// always sees either the previous generation's value for an id not yet
// reached, or the new generation's value for one already reached — never a
// gap. Only AFTER every record has been upserted are stale entries (ids
// that no longer appear at all, e.g. rolled back by a reorg) pruned by
// generation, which is safe precisely because every id that SHOULD survive
// has already been re-stamped with the current generation by that point.
func (s *Service) Reconcile(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64) error {
	var all []*models.ChainEvent
	for _, name := range EventNames {
		evs, err := chainEvents.ListByName(ctx, chainID, s.vaultAddr.Hex(), name)
		if err != nil {
			return fmt.Errorf("sales: list %s events: %w", name, err)
		}
		all = append(all, evs...)
	}

	gen := time.Now().UnixNano()

	for _, p := range BuildPurchases(all) {
		p.Generation = gen
		if err := s.purchases.Upsert(ctx, p); err != nil {
			return fmt.Errorf("sales: upsert purchase %s: %w", p.ID, err)
		}
	}
	if err := s.purchases.DeleteStaleGeneration(ctx, gen); err != nil {
		return fmt.Errorf("sales: prune stale purchases: %w", err)
	}
	return nil
}
