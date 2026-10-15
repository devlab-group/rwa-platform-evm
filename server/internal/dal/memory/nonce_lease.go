package memory

import (
	"context"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- nonce leases ---

type NonceLeaseRepository struct {
	mu sync.Mutex
	m  map[string]*models.NonceLease
}

func NewNonceLeaseRepository() *NonceLeaseRepository {
	return &NonceLeaseRepository{m: map[string]*models.NonceLease{}}
}

// Acquire grants absent or expired leases, retaining expired records so fencing tokens
// cannot restart; see models.NonceLease
func (r *NonceLeaseRepository) Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (uint64, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	existing, ok := r.m[key]
	if ok && existing.ExpiresAt.After(now) {
		return 0, false, nil
	}
	token := uint64(1)
	if ok {
		token = existing.Token + 1
	}
	r.m[key] = &models.NonceLease{Key: key, HolderID: holderID, Token: token, ExpiresAt: now.Add(ttl), UpdatedAt: now}
	return token, true, nil
}

func (r *NonceLeaseRepository) Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[key]
	if !ok || existing.HolderID != holderID || existing.Token != token {
		return false, nil
	}
	existing.ExpiresAt = time.Now().UTC().Add(ttl)
	existing.UpdatedAt = time.Now().UTC()
	return true, nil
}

// Release expires rather than deletes the record, preserving increasing tokens.
// Deletion could reuse a token and let a delayed request from an earlier holder session
// match a later lease.
func (r *NonceLeaseRepository) Release(ctx context.Context, key, holderID string, token uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[key]
	if !ok || existing.HolderID != holderID || existing.Token != token {
		return repository.ErrFencingTokenMismatch
	}
	now := time.Now().UTC()
	existing.ExpiresAt = now
	existing.UpdatedAt = now
	return nil
}
