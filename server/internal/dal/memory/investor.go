package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- investors ---

type InvestorRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Investor
}

func NewInvestorRepository() *InvestorRepository {
	return &InvestorRepository{m: map[string]*models.Investor{}}
}

func (r *InvestorRepository) Get(ctx context.Context, address string) (*models.Investor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[address]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *InvestorRepository) List(ctx context.Context) ([]*models.Investor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Investor, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

// ListPage returns one bounded, ascending-address page. Unlike
// Purchase/RedemptionRequest's ListPage, Address IS the collection's
// unique key already, so the cursor is simply the previous
// page's last address — no separate KeysetCursor/tiebreak is needed (see
// mongodb.investorRepo.ListPage's identical reasoning).
func (r *InvestorRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Investor, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.Investor, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Address < sorted[j].Address })

	start := 0
	if cursor != "" {
		start = len(sorted)
		for i, v := range sorted {
			if v.Address > cursor {
				start = i
				break
			}
		}
	}
	end := start + limit
	if limit <= 0 || end > len(sorted) {
		end = len(sorted)
	}
	page := sorted[start:end]
	next := ""
	if end < len(sorted) {
		next = page[len(page)-1].Address
	}
	return page, next, nil
}

func (r *InvestorRepository) Upsert(ctx context.Context, inv *models.Investor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *inv
	r.m[inv.Address] = &cp
	return nil
}
