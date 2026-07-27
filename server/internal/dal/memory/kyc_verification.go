package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- kyc verifications ---

type KYCVerificationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.KYCVerification
}

func NewKYCVerificationRepository() *KYCVerificationRepository {
	return &KYCVerificationRepository{m: map[string]*models.KYCVerification{}}
}

func (r *KYCVerificationRepository) Upsert(ctx context.Context, v *models.KYCVerification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *v
	cp.ID = models.KYCVerificationID(v.Provider, v.Ref)
	r.m[cp.ID] = &cp
	return nil
}

func (r *KYCVerificationRepository) GetByRef(ctx context.Context, provider, ref string) (*models.KYCVerification, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[models.KYCVerificationID(provider, ref)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}
