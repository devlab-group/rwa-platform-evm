package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- ipfs publications ---

type PublicationRepository struct {
	mu sync.RWMutex
	m  map[string]*models.PublicationRecord
}

func NewPublicationRepository() *PublicationRepository {
	return &PublicationRepository{m: map[string]*models.PublicationRecord{}}
}

func (r *PublicationRepository) Get(ctx context.Context, id string) (*models.PublicationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	cp.Destinations = append([]models.DestinationStatus(nil), v.Destinations...)
	return &cp, nil
}

func (r *PublicationRepository) Upsert(ctx context.Context, rec *models.PublicationRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *rec
	cp.Destinations = append([]models.DestinationStatus(nil), rec.Destinations...)
	r.m[rec.ID] = &cp
	return nil
}

func (r *PublicationRepository) List(ctx context.Context) ([]*models.PublicationRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.PublicationRecord, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		cp.Destinations = append([]models.DestinationStatus(nil), v.Destinations...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
