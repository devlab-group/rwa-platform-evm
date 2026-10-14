package sales

import (
	"context"
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// BuildPurchases converts decoded Vault Purchased chain events into
// models.Purchase records, one per event, since each Purchased log is a
// complete fill
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

// Reconcile rebuilds the purchases read model from every persisted Vault chain
// event for (chainID, vaultAddr), as internal/redemption.Service.Reconcile does.
// It is safe to call at any time, including after an indexer rollback.
//
// The rebuild swaps generations so a concurrent API reader never sees an empty
// or partial history. Every purchase that should exist is upserted (created or
// replaced by id, never deleted first) with a fresh generation. Only after
// all upserts succeed are records from older generations, such as purchases
// rolled back by a reorg, deleted. A crash midway leaves the previous
// records in place.
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
