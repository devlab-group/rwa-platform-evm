package memory

import (
	"context"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Nonce-keyed, single-use admin login challenges

type AdminChallengeRepository struct {
	mu sync.Mutex
	m  map[string]*models.AdminChallenge
}

func NewAdminChallengeRepository() *AdminChallengeRepository {
	return &AdminChallengeRepository{m: map[string]*models.AdminChallenge{}}
}

func (r *AdminChallengeRepository) Insert(ctx context.Context, c *models.AdminChallenge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.m[c.Nonce] = &cp
	return nil
}

func (r *AdminChallengeRepository) Get(ctx context.Context, nonce string) (*models.AdminChallenge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.m[nonce]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if time.Now().UTC().After(c.ExpiresAt) {
		delete(r.m, nonce) // opportunistic eviction, same pattern as the session repos
		return nil, repository.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *AdminChallengeRepository) MarkUsed(ctx context.Context, nonce string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.m[nonce]
	if !ok {
		return repository.ErrNotFound
	}
	if c.Used {
		return repository.ErrAlreadyExists
	}
	c.Used = true
	return nil
}
