package indexer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// atomicIndexer shares memory event/checkpoint stores with its chunk committer
func atomicIndexer(t *testing.T, source *FakeSource, checkpoints *memory.IndexerCheckpointRepository, events *memory.ChainEventRepository, committer repository.ChainChunkRepository) *Indexer {
	t.Helper()
	return New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil,
		WithChunkCommitter(committer))
}

// TestAtomicChunkCommitAdvancesCheckpointAndVersion checks that events and checkpoint
// commit together and advance the version
func TestAtomicChunkCommitAdvancesCheckpointAndVersion(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 5; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(5)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	committer := memory.NewChainChunkRepository(events, blockHashes, checkpoints)
	idx := atomicIndexer(t, source, checkpoints, events, committer)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatalf("checkpoint not saved: %v", err)
	}
	if cp.LastBlock != 5 {
		t.Errorf("LastBlock = %d, want 5", cp.LastBlock)
	}
	if cp.Version != 1 {
		t.Errorf("checkpoint Version = %d, want 1 after one atomic commit", cp.Version)
	}
	evs, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 committed event, got %d", len(evs))
	}
}

// conflictCommitter rejects forward commits and rewinds as stale, simulating a
// competing replica's checkpoint advance
type conflictCommitter struct{}

func (conflictCommitter) CommitChunk(ctx context.Context, c repository.ChunkCommit) (bool, error) {
	return false, nil
}

func (conflictCommitter) RewindChunk(ctx context.Context, r repository.ChunkRewind) (bool, error) {
	return false, nil
}

// TestAtomicChunkCommitVersionConflictStopsCleanly checks that a stale commit makes
// Poll stop without error, event writes or checkpoint advance
func TestAtomicChunkCommitVersionConflictStopsCleanly(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 3; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(3)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := atomicIndexer(t, source, checkpoints, events, conflictCommitter{})

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll should stop cleanly on a version conflict, got: %v", err)
	}
	if _, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress); err == nil {
		t.Fatal("checkpoint must NOT be advanced when the atomic commit lost the version race")
	}
	all, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if len(all) != 0 {
		t.Fatalf("no events may be written when the commit lost the race, got %d", len(all))
	}
}

// forkTo replaces headers from from through head and removes old logs there
func forkTo(source *FakeSource, from, head uint64, seed byte) {
	for i := from; i <= head; i++ {
		source.SetHeader(i, seed)
	}
	source.RemoveLogsAtOrAfter(from)
	source.SetHead(head)
}

// TestAtomicRollbackIsVersionFenced checks event deletion and increasing rewind
// versions so rollback cannot bury concurrent forward commits
func TestAtomicRollbackIsVersionFenced(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 3; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(3)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xforkA"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0A")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := atomicIndexer(t, source, checkpoints, events, memory.NewChainChunkRepository(events, blockHashes, checkpoints))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	cp, _ := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if cp.Version != 1 {
		t.Fatalf("checkpoint Version after the first commit = %d, want 1", cp.Version)
	}

	// reorg at block 2 onto fork B
	forkTo(source, 2, 3, 2)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xforkB"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0B")}})

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll after reorg: %v", err)
	}
	if forkA, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0A").Hex()); len(forkA) != 0 {
		t.Errorf("fork-A events survived the rollback: %d", len(forkA))
	}
	if forkB, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0B").Hex()); len(forkB) != 1 {
		t.Errorf("expected 1 fork-B event after the rollback+rescan, got %d", len(forkB))
	}
	cp, _ = checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if cp.Version < 3 {
		// v1 first commit, v2 rollback rewind, v3 re-scan commit
		t.Errorf("checkpoint Version = %d after rollback+rescan, want it to keep advancing (>=3), never reset", cp.Version)
	}
}

// rewindConflictCommitter accepts forward commits but rejects rewinds, simulating a
// checkpoint advance after rewind calculation
type rewindConflictCommitter struct {
	inner   repository.ChainChunkRepository
	rewinds int
}

func (c *rewindConflictCommitter) CommitChunk(ctx context.Context, cc repository.ChunkCommit) (bool, error) {
	return c.inner.CommitChunk(ctx, cc)
}

func (c *rewindConflictCommitter) RewindChunk(ctx context.Context, r repository.ChunkRewind) (bool, error) {
	c.rewinds++
	return false, nil
}

// TestAtomicRollbackVersionConflictStopsCleanlyAndWritesNothing checks that stale
// rollback preserves the winning events/checkpoint; the next Poll re-reads it and
// detects the reorg again
func TestAtomicRollbackVersionConflictStopsCleanlyAndWritesNothing(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 3; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(3)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xforkA"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0A")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	committer := &rewindConflictCommitter{inner: memory.NewChainChunkRepository(events, blockHashes, checkpoints)}
	idx := atomicIndexer(t, source, checkpoints, events, committer)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	before, _ := checkpoints.Get(context.Background(), chainID, CheckpointAddress)

	forkTo(source, 2, 3, 2)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xforkB"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0B")}})

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll should stop cleanly when the rollback loses the version race, got: %v", err)
	}
	if committer.rewinds == 0 {
		t.Fatal("the reorg did not go through RewindChunk at all")
	}
	after, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastBlock != before.LastBlock || after.Version != before.Version {
		t.Fatalf("a rollback that lost the version race still moved the checkpoint: %d/v%d -> %d/v%d",
			before.LastBlock, before.Version, after.LastBlock, after.Version)
	}
	if forkB, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0B").Hex()); len(forkB) != 0 {
		t.Fatalf("a poll whose rollback lost the race must not go on to ingest, got %d fork-B events", len(forkB))
	}
}

// TestAtomicResetToTrustedCheckpointIsVersionFenced requires trusted reset to advance
// the current version and reject a moved checkpoint, preserving a concurrent replica's
// events
func TestAtomicResetToTrustedCheckpointIsVersionFenced(t *testing.T) {
	newSource := func() *FakeSource {
		source := NewFakeSource()
		for i := uint64(1); i <= 5; i++ {
			source.SetHeader(i, 1)
		}
		source.SetHead(5)
		source.AddLog(types.Log{Address: addr1, BlockNumber: 4, TxHash: common.HexToHash("0xaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0A")}})
		return source
	}

	// happy path: the reset lands and the version advances
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := atomicIndexer(t, newSource(), checkpoints, events, memory.NewChainChunkRepository(events, blockHashes, checkpoints))
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	before, _ := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err := idx.ResetToTrustedCheckpoint(context.Background(), 3); err != nil {
		t.Fatalf("ResetToTrustedCheckpoint: %v", err)
	}
	after, _ := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if after.LastBlock != 3 {
		t.Errorf("LastBlock after reset = %d, want 3", after.LastBlock)
	}
	if after.Version != before.Version+1 {
		t.Errorf("checkpoint Version after reset = %d, want %d (fence must advance, never reset)", after.Version, before.Version+1)
	}
	if orphans, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0A").Hex()); len(orphans) != 0 {
		t.Errorf("the reset left %d events above the trusted block", len(orphans))
	}

	// lost race: the reset must return an error, not force itself through
	checkpoints2 := memory.NewIndexerCheckpointRepository()
	events2 := memory.NewChainEventRepository()
	blockHashes2 := memory.NewBlockHashRepository()
	committer := &rewindConflictCommitter{inner: memory.NewChainChunkRepository(events2, blockHashes2, checkpoints2)}
	idx2 := atomicIndexer(t, newSource(), checkpoints2, events2, committer)
	if err := idx2.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	pre, _ := checkpoints2.Get(context.Background(), chainID, CheckpointAddress)
	err := idx2.ResetToTrustedCheckpoint(context.Background(), 3)
	if err == nil {
		t.Fatal("a reset that lost the version race must return an error, not silently succeed")
	}
	if !strings.Contains(err.Error(), "another indexer replica") {
		t.Errorf("error = %v, want it to explain the concurrent-replica conflict", err)
	}
	post, _ := checkpoints2.Get(context.Background(), chainID, CheckpointAddress)
	if post.LastBlock != pre.LastBlock || post.Version != pre.Version {
		t.Fatalf("a losing reset still moved the checkpoint: %d/v%d -> %d/v%d",
			pre.LastBlock, pre.Version, post.LastBlock, post.Version)
	}
}

// failOnceCommitter writes nothing on its first commit, simulating a transaction
// failure, then delegates to inner for recovery
type failOnceCommitter struct {
	inner  repository.ChainChunkRepository
	failed bool
}

func (c *failOnceCommitter) CommitChunk(ctx context.Context, cc repository.ChunkCommit) (bool, error) {
	if !c.failed {
		c.failed = true
		return false, errors.New("simulated commit transaction abort")
	}
	return c.inner.CommitChunk(ctx, cc)
}

func (c *failOnceCommitter) RewindChunk(ctx context.Context, r repository.ChunkRewind) (bool, error) {
	return c.inner.RewindChunk(ctx, r)
}

// TestAtomicCommitFailureLeavesNoOrphansThenReorg requires a failed poll to leave no
// checkpoint, then rebuild only the new fork. The fake aborts without writing;
// dal/mongodb.TestLiveCommitChunk_FailureMidChunkWritesNothing tests real transaction rollback.
func TestAtomicCommitFailureLeavesNoOrphansThenReorg(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 3; i++ {
		source.SetHeader(i, 1) // fork A
	}
	source.SetHead(3)
	// fork-A log at block 2
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xforkA"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0A")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	committer := &failOnceCommitter{inner: memory.NewChainChunkRepository(events, blockHashes, checkpoints)}
	idx := atomicIndexer(t, source, checkpoints, events, committer)

	// first poll: the commit aborts and nothing may be persisted
	if err := idx.Poll(context.Background()); err == nil {
		t.Fatal("expected the first poll to fail on the aborted commit")
	}
	if _, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress); err == nil {
		t.Fatal("checkpoint must not advance when the commit aborted")
	}
	forkA, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0A").Hex())
	if len(forkA) != 0 {
		t.Fatalf("aborted commit left %d orphaned fork-A events", len(forkA))
	}

	// Reorganize block 2 onward to fork B, replacing its headers and logs
	source.SetHeader(2, 2)
	source.SetHeader(3, 2)
	source.RemoveLogsAtOrAfter(2)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xforkB"), Index: 0, Topics: []common.Hash{common.HexToHash("0x0B")}})

	// second poll: the committer now succeeds and only fork-B events land
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	forkA, _ = events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0A").Hex())
	if len(forkA) != 0 {
		t.Fatalf("fork-A orphans survived the reorg: %d", len(forkA))
	}
	forkB, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x0B").Hex())
	if len(forkB) != 1 {
		t.Fatalf("expected exactly one fork-B event after recovery, got %d", len(forkB))
	}
}
