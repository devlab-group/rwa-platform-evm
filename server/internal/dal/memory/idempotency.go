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

// newFencingToken generates a random per-reservation fencing/lease token —
// 16 bytes is ample entropy for a value whose
// only job is to distinguish "the caller that currently owns this
// reservation" from a stale/superseded one; it is never parsed or compared
// for anything beyond byte-for-byte equality.
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

// Reserve is atomic under r.mu: exactly one caller observes (nil, true,
// token, nil) for a given key at a time. An existing record whose
// ExpiresAt has already passed is treated as free rather than returned —
// this is the "expired-reservation takeover" behavior: without it, crashed
// pending reservations remain stuck forever because ExpiresAt is written but
// never enforced. A reservation nobody Complete()d/Release()d within ttl
// no longer blocks a fresh attempt, bounding how long a crash can wedge a
// key to ttl instead of forever. Because the whole check-then-overwrite
// happens under one lock, a second caller racing in immediately after
// never observes the stale expired record either — it sees the new
// caller's fresh pending entry and correctly gets (existing, false, "",
// nil).
//
// Every winning Reserve mints a fresh random token and
// stores it on the new record; Complete/Release below refuse to act
// unless the caller presents that exact token, so a caller whose
// reservation already got taken over by a later Reserve (it was slow
// enough for ttl to elapse) can no longer finish/cancel the NEW owner's
// reservation out from under it.
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

// Complete matches the existing record by key AND requires token to equal
// the record's current fencing token: a caller so slow that ttl elapsed and
// a second caller has already Reserve()d and possibly Complete()d the same
// key presents a now-stale token here and is rejected with
// ErrFencingTokenMismatch instead of silently overwriting the new owner's
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

// Release deletes the reservation for key only if token matches its
// current fencing token — same reasoning as Complete: a stale/superseded
// owner must not be able to delete a later owner's live reservation out
// from under it.
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
