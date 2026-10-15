package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// newFencingToken creates a random 128-bit reservation ID, compared only for equality
func newFencingToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- idempotency ---

type IdempotencyRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IdempotencyRecord
}

func NewIdempotencyRepository() *IdempotencyRepository {
	return &IdempotencyRepository{m: map[string]*models.IdempotencyRecord{}}
}

func (r *IdempotencyRepository) Get(ctx context.Context, key string) (*models.IdempotencyRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[key]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

// Reserve checks expiry and claims under r.mu, returning a fresh token to the winner.
// TTL permits abandoned-claim takeover; token checks reject superseded Complete/Release
// calls. See repository.IdempotencyRepository.
func (r *IdempotencyRepository) Reserve(ctx context.Context, key, method, path, requestHash string, ttl time.Duration) (*models.IdempotencyRecord, bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	if existing, ok := r.m[key]; ok && existing.ExpiresAt.After(now) {
		cp := *existing
		cp.Token = "" // never hand a live reservation's fencing token to a caller that didn't win it
		return &cp, false, "", nil
	}
	token, err := newFencingToken()
	if err != nil {
		return nil, false, "", err
	}
	r.m[key] = &models.IdempotencyRecord{
		Key: key, Method: method, Path: path, RequestHash: requestHash, Token: token,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	return nil, true, token, nil
}

// Complete returns ErrFencingTokenMismatch if takeover changed the stored token,
// preventing a slow caller from overwriting the new owner's response
func (r *IdempotencyRepository) Complete(ctx context.Context, key, token string, status int, body []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.m[key]
	if !ok || rec.Token != token {
		return repository.ErrFencingTokenMismatch
	}
	rec.ResponseStatus = status
	rec.ResponseBody = body
	return nil
}

// Release deletes only a matching-token reservation, protecting later owners from stale callers
func (r *IdempotencyRepository) Release(ctx context.Context, key, token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.m[key]
	if !ok || rec.Token != token {
		return repository.ErrFencingTokenMismatch
	}
	delete(r.m, key)
	return nil
}
