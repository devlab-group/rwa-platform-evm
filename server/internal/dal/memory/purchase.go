package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- purchases ---

type PurchaseRepository struct {
	mu sync.RWMutex
	l  []*models.Purchase
}

func NewPurchaseRepository() *PurchaseRepository { return &PurchaseRepository{} }

func (r *PurchaseRepository) Create(ctx context.Context, p *models.Purchase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.l = append(r.l, &cp)
	return nil
}

func (r *PurchaseRepository) List(ctx context.Context) ([]*models.Purchase, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Purchase, len(r.l))
	copy(out, r.l)
	return out, nil
}

func (r *PurchaseRepository) DeleteAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.l = nil
	return nil
}

// Upsert is the reader-safe half of a generation-swap rebuild —
// see the interface doc comment.
func (r *PurchaseRepository) Upsert(ctx context.Context, p *models.Purchase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	for i, existing := range r.l {
		if existing.ID == p.ID {
			r.l[i] = &cp
			return nil
		}
	}
	r.l = append(r.l, &cp)
	return nil
}

// DeleteStaleGeneration is the cleanup half — see the interface
// doc comment.
func (r *PurchaseRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]*models.Purchase, 0, len(r.l))
	for _, p := range r.l {
		if p.Generation == gen {
			kept = append(kept, p)
		}
	}
	r.l = kept
	return nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Newest-block-first, matching purchaseRepo's mongodb sort.
func (r *PurchaseRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Purchase, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.Purchase, len(r.l))
	copy(sorted, r.l)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BlockNumber != sorted[j].BlockNumber {
			return sorted[i].BlockNumber > sorted[j].BlockNumber
		}
		return sorted[i].ID > sorted[j].ID
	})
	page, next := repository.KeysetPage(sorted,
		func(p *models.Purchase) int64 { return int64(p.BlockNumber) },
		func(p *models.Purchase) string { return p.ID },
		cursor, limit, true)
	out := make([]*models.Purchase, len(page))
	for i, p := range page {
		cp := *p
		out[i] = &cp
	}
	return out, next, nil
}
