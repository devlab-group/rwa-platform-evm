package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- indexer block hashes ---

type BlockHashRepository struct {
	mu sync.RWMutex
	m  map[int64]map[uint64]string
}

func NewBlockHashRepository() *BlockHashRepository {
	return &BlockHashRepository{m: map[int64]map[uint64]string{}}
}

func (r *BlockHashRepository) Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[chainID] == nil {
		r.m[chainID] = map[uint64]string{}
	}
	r.m[chainID][blockNumber] = hash
	return nil
}

func (r *BlockHashRepository) Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.m[chainID][blockNumber]
	if !ok {
		return "", repository.ErrNotFound
	}
	return h, nil
}

func (r *BlockHashRepository) PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for bn := range r.m[chainID] {
		if bn < keepFrom {
			delete(r.m[chainID], bn)
		}
	}
	return nil
}
