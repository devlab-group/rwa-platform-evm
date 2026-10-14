package memory

import (
	"context"
	"sync"

	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- indexer atomic chunk commit ---

// ChainChunkRepository commits a scanned chunk over the in-memory event, block-hash
// and checkpoint repositories. Its mutex, held across the version check and every
// write, makes the commit atomic, where the MongoDB implementation uses a
// transaction. In atomic mode the indexer changes the checkpoint only through
// this repository, so nothing else races the version check under the lock.
type ChainChunkRepository struct {
	mu          sync.Mutex
	events      *ChainEventRepository
	blockHashes *BlockHashRepository
	checkpoints *IndexerCheckpointRepository
}

func NewChainChunkRepository(events *ChainEventRepository, blockHashes *BlockHashRepository, checkpoints *IndexerCheckpointRepository) *ChainChunkRepository {
	return &ChainChunkRepository{events: events, blockHashes: blockHashes, checkpoints: checkpoints}
}

func (r *ChainChunkRepository) CommitChunk(ctx context.Context, c repository.ChunkCommit) (bool, error) {
	if c.Checkpoint == nil {
		return false, repository.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	curVersion := 0
	if cur, err := r.checkpoints.Get(ctx, c.Checkpoint.ChainID, c.Checkpoint.Address); err == nil {
		curVersion = cur.Version
	} else if err != repository.ErrNotFound {
		return false, err
	}
	if curVersion != c.ExpectedCheckpointVersion {
		return false, nil // a concurrent writer advanced the checkpoint; caller lost the race
	}

	for _, e := range c.Events {
		if err := r.events.Create(ctx, e); err != nil {
			return false, err
		}
	}
	for _, bh := range c.BlockHashes {
		if err := r.blockHashes.Record(ctx, c.ChainID, bh.BlockNumber, bh.Hash); err != nil {
			return false, err
		}
	}
	if c.PruneBlockHashesBefore != nil {
		if err := r.blockHashes.PruneBefore(ctx, c.ChainID, *c.PruneBlockHashesBefore); err != nil {
			return false, err
		}
	}
	cp := *c.Checkpoint
	cp.Version = c.ExpectedCheckpointVersion + 1
	if err := r.checkpoints.Set(ctx, &cp); err != nil {
		return false, err
	}
	return true, nil
}

// RewindChunk applies a reorg rollback or trusted reset under the same lock and
// version check as CommitChunk. See repository.ChainChunkRepository.RewindChunk.
func (r *ChainChunkRepository) RewindChunk(ctx context.Context, rw repository.ChunkRewind) (bool, error) {
	if rw.Checkpoint == nil {
		return false, repository.ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	curVersion := 0
	if cur, err := r.checkpoints.Get(ctx, rw.Checkpoint.ChainID, rw.Checkpoint.Address); err == nil {
		curVersion = cur.Version
	} else if err != repository.ErrNotFound {
		return false, err
	}
	if curVersion != rw.ExpectedCheckpointVersion {
		return false, nil // a concurrent writer moved the checkpoint; recompute the rewind
	}

	for _, addr := range rw.Addresses {
		if _, err := r.events.DeleteFromBlock(ctx, rw.ChainID, addr, rw.DeleteFromBlock); err != nil {
			return false, err
		}
	}
	cp := *rw.Checkpoint
	cp.Version = rw.ExpectedCheckpointVersion + 1
	if err := r.checkpoints.Set(ctx, &cp); err != nil {
		return false, err
	}
	return true, nil
}
