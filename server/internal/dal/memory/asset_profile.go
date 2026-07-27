package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- asset profiles ---

type AssetProfileRepository struct {
	mu sync.RWMutex
	m  map[string]*models.AssetProfile
}

func NewAssetProfileRepository() *AssetProfileRepository {
	return &AssetProfileRepository{m: map[string]*models.AssetProfile{}}
}

func (r *AssetProfileRepository) Get(ctx context.Context, projectID string) (*models.AssetProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[projectID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

// GetCurrent returns the most-recent (by CreatedAt) stored profile, or
// repository.ErrNotFound when none exists — the single-tenant "the profile"
// accessor backing GET /api/v1/profile.
func (r *AssetProfileRepository) GetCurrent(ctx context.Context) (*models.AssetProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var current *models.AssetProfile
	for _, v := range r.m {
		if current == nil || v.CreatedAt.After(current.CreatedAt) {
			current = v
		}
	}
	if current == nil {
		return nil, repository.ErrNotFound
	}
	cp := *current
	return &cp, nil
}

func (r *AssetProfileRepository) Upsert(ctx context.Context, p *models.AssetProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.m[p.ProjectID] = &cp
	return nil
}

// Create is create-once/CAS: it refuses to overwrite an existing profile
// for the same projectId (see the interface doc comment).
func (r *AssetProfileRepository) Create(ctx context.Context, p *models.AssetProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[p.ProjectID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *p
	r.m[p.ProjectID] = &cp
	return nil
}
