package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- audit packages ---

type AuditPackageRepository struct {
	mu sync.RWMutex
	m  map[string]*models.AuditPackage
}

func NewAuditPackageRepository() *AuditPackageRepository {
	return &AuditPackageRepository{m: map[string]*models.AuditPackage{}}
}

func (r *AuditPackageRepository) Get(ctx context.Context, recordID string) (*models.AuditPackage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[recordID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *AuditPackageRepository) Upsert(ctx context.Context, p *models.AuditPackage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	r.m[p.RecordID] = &cp
	return nil
}
