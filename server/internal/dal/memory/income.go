package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type IncomePreparationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IncomePreparation
}

func NewIncomePreparationRepository() *IncomePreparationRepository {
	return &IncomePreparationRepository{m: map[string]*models.IncomePreparation{}}
}

func (r *IncomePreparationRepository) Create(ctx context.Context, p *models.IncomePreparation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[p.ID]; ok {
		return repository.ErrAlreadyExists
	}
	// mirrors the Mongo unique index on (merkleRoot, snapshotSha256)
	for _, v := range r.m {
		if v.MerkleRoot == p.MerkleRoot && v.SnapshotSha256 == p.SnapshotSha256 {
			return repository.ErrAlreadyExists
		}
	}
	cp := *p
	r.m[p.ID] = &cp
	return nil
}

func (r *IncomePreparationRepository) Get(ctx context.Context, id string) (*models.IncomePreparation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *IncomePreparationRepository) SetClaimDeadline(ctx context.Context, id string, deadline int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[id]
	if !ok {
		return repository.ErrNotFound
	}
	v.ClaimDeadline = deadline
	return nil
}

// List sorts oldest first, by (CreatedAt, ID), without SnapshotRaw
func (r *IncomePreparationRepository) List(ctx context.Context) ([]*models.IncomePreparation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.IncomePreparation, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		cp.SnapshotRaw = nil
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

type IncomeDistributionRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IncomeDistribution
}

func NewIncomeDistributionRepository() *IncomeDistributionRepository {
	return &IncomeDistributionRepository{m: map[string]*models.IncomeDistribution{}}
}

func (r *IncomeDistributionRepository) Upsert(ctx context.Context, d *models.IncomeDistribution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *d
	r.m[d.ID] = &cp
	return nil
}

// List sorts in creation order, by (CreatedBlock, CreatedLogIndex)
func (r *IncomeDistributionRepository) List(ctx context.Context) ([]*models.IncomeDistribution, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.IncomeDistribution, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedBlock != out[j].CreatedBlock {
			return out[i].CreatedBlock < out[j].CreatedBlock
		}
		return out[i].CreatedLogIndex < out[j].CreatedLogIndex
	})
	return out, nil
}

func (r *IncomeDistributionRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.m {
		if v.Generation != gen {
			delete(r.m, id)
		}
	}
	return nil
}

type IncomeClaimRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IncomeClaim
}

func NewIncomeClaimRepository() *IncomeClaimRepository {
	return &IncomeClaimRepository{m: map[string]*models.IncomeClaim{}}
}

func (r *IncomeClaimRepository) Upsert(ctx context.Context, c *models.IncomeClaim) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.m[c.ID] = &cp
	return nil
}

func (r *IncomeClaimRepository) ListByAccount(ctx context.Context, account string) ([]*models.IncomeClaim, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.IncomeClaim
	for _, v := range r.m {
		if v.Account == strings.ToLower(account) {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *IncomeClaimRepository) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, v := range r.m {
		if v.Generation != gen {
			delete(r.m, id)
		}
	}
	return nil
}
