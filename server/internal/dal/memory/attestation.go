package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- attestations ---

type AttestationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Attestation
}

func NewAttestationRepository() *AttestationRepository {
	return &AttestationRepository{m: map[string]*models.Attestation{}}
}

func (r *AttestationRepository) Get(ctx context.Context, recordID string) (*models.Attestation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[recordID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *AttestationRepository) Upsert(ctx context.Context, a *models.Attestation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.m[a.RecordID] = &cp
	return nil
}
