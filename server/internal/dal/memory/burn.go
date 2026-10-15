package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type BurnRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Burn
}

func NewBurnRepository() *BurnRepository {
	return &BurnRepository{m: map[string]*models.Burn{}}
}

func (r *BurnRepository) Get(ctx context.Context, burnID string) (*models.Burn, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[burnID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *BurnRepository) GetByOperationID(ctx context.Context, operationID string) (*models.Burn, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.m {
		if strings.EqualFold(v.OperationID, operationID) {
			cp := *v
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *BurnRepository) ListByRecord(ctx context.Context, recordID string) ([]*models.Burn, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.Burn
	for _, v := range r.m {
		if v.RecordID == recordID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *BurnRepository) Create(ctx context.Context, b *models.Burn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[b.BurnID]; ok {
		return repository.ErrAlreadyExists
	}
	for _, v := range r.m {
		if strings.EqualFold(v.OperationID, b.OperationID) {
			return repository.ErrAlreadyExists
		}
	}
	cp := *b
	r.m[b.BurnID] = &cp
	return nil
}

// UpdateConditional checks and writes the version under the same lock
func (r *BurnRepository) UpdateConditional(ctx context.Context, b *models.Burn, expectedVersion int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[b.BurnID]
	if !ok {
		return false, repository.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return false, nil
	}
	cp := *b
	cp.Version = expectedVersion + 1
	r.m[b.BurnID] = &cp
	return true, nil
}

// ListPage sorts by (CreatedAt, BurnID) descending
func (r *BurnRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Burn, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.Burn, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
		}
		return sorted[i].BurnID > sorted[j].BurnID
	})
	page, next := repository.KeysetPage(sorted,
		func(b *models.Burn) int64 { return b.CreatedAt.UnixNano() },
		func(b *models.Burn) string { return b.BurnID },
		cursor, limit, true)
	return page, next, nil
}
