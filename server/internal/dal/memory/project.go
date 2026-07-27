package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- projects ---

type ProjectRepository struct {
	mu sync.RWMutex
	p  *models.Project
}

func NewProjectRepository() *ProjectRepository { return &ProjectRepository{} }

func (r *ProjectRepository) Get(ctx context.Context) (*models.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.p == nil {
		return nil, repository.ErrNotFound
	}
	cp := *r.p
	return &cp, nil
}

func (r *ProjectRepository) Upsert(ctx context.Context, p *models.Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.p = &cp
	return nil
}
