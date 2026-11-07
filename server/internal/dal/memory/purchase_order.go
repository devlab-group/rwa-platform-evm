package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// PurchaseOrderRepository is the in-memory repository.PurchaseOrderRepository
type PurchaseOrderRepository struct {
	mu sync.RWMutex
	m  map[string]*models.PurchaseOrder
}

func NewPurchaseOrderRepository() *PurchaseOrderRepository {
	return &PurchaseOrderRepository{m: map[string]*models.PurchaseOrder{}}
}

func (r *PurchaseOrderRepository) Upsert(ctx context.Context, o *models.PurchaseOrder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *o
	r.m[o.ID] = &cp
	return nil
}

func (r *PurchaseOrderRepository) Get(ctx context.Context, id string) (*models.PurchaseOrder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *PurchaseOrderRepository) DeleteAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = map[string]*models.PurchaseOrder{}
	return nil
}

// DeleteStaleGeneration is the cleanup step of the generation-swap rebuild, see the
// interface doc comment
func (r *PurchaseOrderRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.m {
		if v.Generation != gen {
			delete(r.m, id)
		}
	}
	return nil
}

// ListPage returns one page, newest CreatedAt first, as in mongodb.purchaseOrderRepo
func (r *PurchaseOrderRepository) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.PurchaseOrder, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addrLower := strings.ToLower(address)
	var sorted []*models.PurchaseOrder
	for _, v := range r.m {
		if status != "" && string(v.Status) != status {
			continue
		}
		if addrLower != "" && strings.ToLower(v.Buyer) != addrLower {
			continue
		}
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt != sorted[j].CreatedAt {
			return sorted[i].CreatedAt > sorted[j].CreatedAt
		}
		return sorted[i].ID > sorted[j].ID
	})
	page, next := repository.KeysetPage(sorted,
		func(o *models.PurchaseOrder) int64 { return o.CreatedAt },
		func(o *models.PurchaseOrder) string { return o.ID },
		cursor, limit, true)
	return page, next, nil
}
