package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
)

// --- chain events ---

type ChainEventRepository struct {
	mu sync.RWMutex
	m  map[models.EventKey]*models.ChainEvent
}

func NewChainEventRepository() *ChainEventRepository {
	return &ChainEventRepository{m: map[models.EventKey]*models.ChainEvent{}}
}

func (r *ChainEventRepository) Exists(ctx context.Context, key models.EventKey) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.m[key]
	return ok, nil
}

func (r *ChainEventRepository) Create(ctx context.Context, e *models.ChainEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := models.EventKey{ChainID: e.ChainID, Address: e.Address, TxHash: e.TxHash, LogIndex: e.LogIndex}
	cp := *e
	r.m[key] = &cp
	return nil
}

func (r *ChainEventRepository) DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, v := range r.m {
		if v.ChainID == chainID && v.Address == address && v.BlockNumber >= fromBlock {
			delete(r.m, k)
			n++
		}
	}
	return n, nil
}

func (r *ChainEventRepository) ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.ChainEvent, 0)
	for _, v := range r.m {
		if v.ChainID == chainID && v.Address == address && v.Name == name {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BlockNumber != out[j].BlockNumber {
			return out[i].BlockNumber < out[j].BlockNumber
		}
		return out[i].LogIndex < out[j].LogIndex
	})
	return out, nil
}

func (r *ChainEventRepository) ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.ChainEvent, 0)
	for _, v := range r.m {
		if v.ChainID == chainID {
			cp := *v
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BlockNumber != out[j].BlockNumber {
			return out[i].BlockNumber < out[j].BlockNumber
		}
		if out[i].LogIndex != out[j].LogIndex {
			return out[i].LogIndex < out[j].LogIndex
		}
		return out[i].TxHash < out[j].TxHash
	})
	return out, nil
}
