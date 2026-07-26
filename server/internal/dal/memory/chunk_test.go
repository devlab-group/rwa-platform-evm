package memory

import (
	"context"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// TestChainChunkCommitConditionalVersion is the conditional-commit
// regression at the repository boundary: CommitChunk succeeds only when the
// stored checkpoint version matches ExpectedCheckpointVersion, bumps it on
// success, and writes NOTHING (no orphaned events, no checkpoint change) when
// a concurrent writer already advanced it.
func TestChainChunkCommitConditionalVersion(t *testing.T) {
	ctx := context.Background()
	events := NewChainEventRepository()
	blockHashes := NewBlockHashRepository()
	checkpoints := NewIndexerCheckpointRepository()
	repo := NewChainChunkRepository(events, blockHashes, checkpoints)

	ev := func(tx string) *models.ChainEvent {
		return &models.ChainEvent{ChainID: 1, Address: "0xabc", TxHash: tx, LogIndex: 0, BlockNumber: 2, Name: "X"}
	}
	cp := func(block uint64) *models.IndexerCheckpoint {
		return &models.IndexerCheckpoint{ChainID: 1, Address: "ALL", LastBlock: block, LastBlockHash: "0xh", UpdatedAt: time.Now()}
	}

	// First commit layered on version 0 succeeds and bumps to version 1.
	ok, err := repo.CommitChunk(ctx, repository.ChunkCommit{
		ChainID: 1, Events: []*models.ChainEvent{ev("0x1")}, Checkpoint: cp(5), ExpectedCheckpointVersion: 0,
	})
	if err != nil || !ok {
		t.Fatalf("first CommitChunk = %v,%v; want true,nil", ok, err)
	}
	stored, err := checkpoints.Get(ctx, 1, "ALL")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 || stored.LastBlock != 5 {
		t.Fatalf("checkpoint = version %d block %d, want version 1 block 5", stored.Version, stored.LastBlock)
	}

	// A stale commit still expecting version 0 must be rejected and write nothing.
	ok, err = repo.CommitChunk(ctx, repository.ChunkCommit{
		ChainID: 1, Events: []*models.ChainEvent{ev("0xorphan")}, Checkpoint: cp(9), ExpectedCheckpointVersion: 0,
	})
	if err != nil {
		t.Fatalf("stale CommitChunk err = %v", err)
	}
	if ok {
		t.Fatal("a commit layered on a stale version must NOT succeed")
	}
	stored, _ = checkpoints.Get(ctx, 1, "ALL")
	if stored.Version != 1 || stored.LastBlock != 5 {
		t.Fatalf("checkpoint changed under a losing commit: version %d block %d", stored.Version, stored.LastBlock)
	}
	if exists, _ := events.Exists(ctx, models.EventKey{ChainID: 1, Address: "0xabc", TxHash: "0xorphan", LogIndex: 0}); exists {
		t.Fatal("a losing commit must not write its events (orphan leak)")
	}

	// A commit layered on the CURRENT version (1) succeeds again -> version 2.
	ok, err = repo.CommitChunk(ctx, repository.ChunkCommit{
		ChainID: 1, Events: []*models.ChainEvent{ev("0x2")}, Checkpoint: cp(9), ExpectedCheckpointVersion: 1,
	})
	if err != nil || !ok {
		t.Fatalf("version-1 CommitChunk = %v,%v; want true,nil", ok, err)
	}
	stored, _ = checkpoints.Get(ctx, 1, "ALL")
	if stored.Version != 2 || stored.LastBlock != 9 {
		t.Fatalf("checkpoint = version %d block %d, want version 2 block 9", stored.Version, stored.LastBlock)
	}
}
