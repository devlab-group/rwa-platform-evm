package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- redemption requests ---

type RedemptionRequestRepository struct {
	mu sync.RWMutex
	m  map[string]*models.RedemptionRequest
}

func NewRedemptionRequestRepository() *RedemptionRequestRepository {
	return &RedemptionRequestRepository{m: map[string]*models.RedemptionRequest{}}
}

func (r *RedemptionRequestRepository) Upsert(ctx context.Context, req *models.RedemptionRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *req
	r.m[req.ID] = &cp
	return nil
}

func (r *RedemptionRequestRepository) Get(ctx context.Context, id string) (*models.RedemptionRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *RedemptionRequestRepository) List(ctx context.Context, status string) ([]*models.RedemptionRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.RedemptionRequest, 0, len(r.m))
	for _, v := range r.m {
		if status != "" && string(v.Status) != status {
			continue
		}
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func (r *RedemptionRequestRepository) DeleteAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = map[string]*models.RedemptionRequest{}
	return nil
}

// DeleteStaleGeneration is the cleanup half of a generation-swap
// rebuild — see the interface doc comment.
func (r *RedemptionRequestRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.m {
		if v.Generation != gen {
			delete(r.m, id)
		}
	}
	return nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Newest-CreatedAt-first, matching redemptionRequestRepo's
// mongodb sort.
func (r *RedemptionRequestRepository) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.RedemptionRequest, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addrLower := strings.ToLower(address)
	var sorted []*models.RedemptionRequest
	for _, v := range r.m {
		if status != "" && string(v.Status) != status {
			continue
		}
		if addrLower != "" && strings.ToLower(v.Beneficiary) != addrLower {
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
		func(r *models.RedemptionRequest) int64 { return r.CreatedAt },
		func(r *models.RedemptionRequest) string { return r.ID },
		cursor, limit, true)
	return page, next, nil
}
