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

// Acquire grants the lease IFF free or expired, regardless of who
// previously held it (see the interface doc comment). Deliberately never
// deletes an expired entry — see models.NonceLease's doc comment on why a
// reset Token would break the fencing guarantee.
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

// Release marks the record immediately expired (free for the next
// Acquire) rather than deleting it — deleting would let the NEXT Acquire's
// existing.Token+1 computation start back over from 0/1, resetting the
// fencing token exactly like an unwanted TTL-based auto-delete would (see
// models.NonceLease's doc comment): a network-delayed, stale write from
// THIS SAME holder's earlier (now-released) session could then collide
// with a later session that reused the same reset token — the classic
// fencing-token ABA problem. Keeping the record (with its token
// preserved) means every future Acquire for key still computes a value
// strictly greater than anything ever issued for it.
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
