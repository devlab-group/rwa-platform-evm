package memory

import (
	"context"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- wallet sessions ---

type WalletSessionRepository struct {
	mu sync.Mutex
	m  map[string]*models.WalletSession
}

func NewWalletSessionRepository() *WalletSessionRepository {
	return &WalletSessionRepository{m: map[string]*models.WalletSession{}}
}

func (r *WalletSessionRepository) Create(ctx context.Context, s *models.WalletSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.m[s.Token] = &cp
	return nil
}

func (r *WalletSessionRepository) Get(ctx context.Context, token string) (*models.WalletSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[token]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		delete(r.m, token) // opportunistic eviction, same pattern as the old in-process SessionManager
		return nil, repository.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *WalletSessionRepository) Delete(ctx context.Context, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, token)
	return nil
}
