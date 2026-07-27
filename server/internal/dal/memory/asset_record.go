package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- asset records ---

type AssetRecordRepository struct {
	mu sync.RWMutex
	m  map[string]*models.AssetRecord
}

func NewAssetRecordRepository() *AssetRecordRepository {
	return &AssetRecordRepository{m: map[string]*models.AssetRecord{}}
}

func (r *AssetRecordRepository) List(ctx context.Context) ([]*models.AssetRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.AssetRecord, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Ascending-CreatedAt-first, matching assetRecordRepo's mongodb
// sort.
func (r *AssetRecordRepository) ListPage(ctx context.Context, cursor string, limit int) ([]*models.AssetRecord, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sorted := make([]*models.AssetRecord, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].RecordID < sorted[j].RecordID
	})
	page, next := repository.KeysetPage(sorted,
		func(r *models.AssetRecord) int64 { return r.CreatedAt.UnixNano() },
		func(r *models.AssetRecord) string { return r.RecordID },
		cursor, limit, false)
	return page, next, nil
}

func (r *AssetRecordRepository) Get(ctx context.Context, recordID string) (*models.AssetRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[recordID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *AssetRecordRepository) Create(ctx context.Context, rec *models.AssetRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[rec.RecordID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *rec
	r.m[rec.RecordID] = &cp
	return nil
}

func (r *AssetRecordRepository) Update(ctx context.Context, rec *models.AssetRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[rec.RecordID]; !ok {
		return repository.ErrNotFound
	}
	cp := *rec
	r.m[rec.RecordID] = &cp
	return nil
}

// UpdateConditional is the storage-level CAS — see the interface
// doc comment. The mutex makes the version check and write atomic.
func (r *AssetRecordRepository) UpdateConditional(ctx context.Context, rec *models.AssetRecord, expectedVersion int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[rec.RecordID]
	if !ok {
		return false, repository.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return false, nil
	}
	cp := *rec
	cp.Version = expectedVersion + 1
	r.m[rec.RecordID] = &cp
	return true, nil
}
