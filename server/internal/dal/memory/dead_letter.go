package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- indexer dead letters ---

type DeadLetterRepository struct {
	mu sync.RWMutex
	m  map[string]*models.DeadLetterEntry
}

func NewDeadLetterRepository() *DeadLetterRepository {
	return &DeadLetterRepository{m: map[string]*models.DeadLetterEntry{}}
}

func (r *DeadLetterRepository) Record(ctx context.Context, e *models.DeadLetterEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	if existing, ok := r.m[e.ID]; ok {
		cp.FirstFailedAt = existing.FirstFailedAt
		cp.RetryCount = existing.RetryCount + 1
		cp.Resolved = false // a fresh failure un-resolves a previously-resolved entry
	}
	r.m[e.ID] = &cp
	return nil
}

func (r *DeadLetterRepository) Get(ctx context.Context, id string) (*models.DeadLetterEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (r *DeadLetterRepository) List(ctx context.Context) ([]*models.DeadLetterEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.DeadLetterEntry, 0, len(r.m))
	for _, e := range r.m {
		cp := *e
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstFailedAt.Before(out[j].FirstFailedAt) })
	return out, nil
}

func (r *DeadLetterRepository) Resolve(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[id]
	if !ok {
		return repository.ErrNotFound
	}
	e.Resolved = true
	return nil
}
