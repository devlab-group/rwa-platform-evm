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

// newFencingToken returns a random 128-bit token that identifies one reservation.
// It is only ever compared for equality.
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

// Reserve runs under r.mu, so exactly one caller gets (nil, true, token, nil) for a
// key at a time. A record whose ExpiresAt has passed is treated as free, so a
// reservation that nobody completed or released within ttl blocks the key for ttl
// at most. Because the check and the overwrite happen under one lock, a second
// caller arriving right after sees the new pending record and gets (existing,
// false, "", nil).
//
// Each winning Reserve stores a new random token on the record. Complete and
// Release act only with that token, so a caller whose reservation was taken over
// cannot finish or cancel the new owner's reservation.
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

// Complete requires the record's current token to equal token. A caller so slow
// that ttl elapsed and another caller reserved, and possibly completed, the same
// key gets ErrFencingTokenMismatch and does not overwrite the new owner's
// response.
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

// Release deletes the reservation for key only if token matches the current one,
// so a superseded owner cannot delete a later owner's reservation
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
