package sales

import (
	"context"
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// BuildPurchases converts decoded Vault Purchased events into models.Purchase records,
// one per complete fill
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

// Reconcile rebuilds purchases from persisted Vault events for (chainID, vaultAddr),
// reflecting indexer rollbacks as internal/redemption.Service.Reconcile does.
//
// Upsert every fill under a fresh generation before pruning older rows, avoiding
// delete-first gaps for surviving purchases. Readers can see mixed generations; a crash
// leaves old rows alongside completed upserts.
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
