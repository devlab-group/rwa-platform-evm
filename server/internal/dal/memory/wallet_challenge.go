package memory

import (
	"context"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- wallet challenges ---

type WalletChallengeRepository struct {
	mu sync.RWMutex
	m  map[string]*models.WalletChallenge
}

func NewWalletChallengeRepository() *WalletChallengeRepository {
	return &WalletChallengeRepository{m: map[string]*models.WalletChallenge{}}
}

func (r *WalletChallengeRepository) Create(ctx context.Context, c *models.WalletChallenge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[c.ID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *c
	r.m[c.ID] = &cp
	return nil
}

func (r *WalletChallengeRepository) Get(ctx context.Context, id string) (*models.WalletChallenge, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *WalletChallengeRepository) MarkUsed(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[id]
	if !ok {
		return repository.ErrNotFound
	}
	if v.Used {
		return repository.ErrAlreadyExists
	}
	v.Used = true
	return nil
}

// CountActive is the memory-backed half of the per-address active-
// challenge cap: a linear scan is fine here since the in-memory repository
// only ever backs unit tests / local dev, never a production deployment's
// actual traffic volume (Mongo's indexed CountDocuments handles that case —
// see internal/dal/mongodb/compliance.go).
func (r *WalletChallengeRepository) CountActive(ctx context.Context, address string, now time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, v := range r.m {
		if v.Address == address && !v.Used && v.ExpiresAt.After(now) {
			n++
		}
	}
	return n, nil
}
