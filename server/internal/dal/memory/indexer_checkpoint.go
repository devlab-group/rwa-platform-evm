package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- indexer checkpoints ---

type IndexerCheckpointRepository struct {
	mu sync.RWMutex
	m  map[string]*models.IndexerCheckpoint
}

func NewIndexerCheckpointRepository() *IndexerCheckpointRepository {
	return &IndexerCheckpointRepository{m: map[string]*models.IndexerCheckpoint{}}
}

func checkpointKey(chainID int64, address string) string {
	return fmt.Sprintf("%d:%s", chainID, address)
}

func (r *IndexerCheckpointRepository) Get(ctx context.Context, chainID int64, address string) (*models.IndexerCheckpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[checkpointKey(chainID, address)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *IndexerCheckpointRepository) Set(ctx context.Context, c *models.IndexerCheckpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.m[checkpointKey(c.ChainID, c.Address)] = &cp
	return nil
}
