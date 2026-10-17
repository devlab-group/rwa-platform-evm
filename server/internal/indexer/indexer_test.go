package indexer

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

// noSleep replaces the indexer's backoff wait so retry tests run instantly
func noSleep(time.Duration) {}

const chainID = 31337

var addr1 = common.HexToAddress("0x0000000000000000000000000000000000A001")

func newTestIndexer(t *testing.T, source *FakeSource, checkpoints *memory.IndexerCheckpointRepository, events *memory.ChainEventRepository, opts ...Option) *Indexer {
	t.Helper()
	return New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil, opts...)
}

func TestIndexerIngestsNewLogsAndAdvancesCheckpoint(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 5; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(5)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.AddLog(types.Log{Address: addr1, BlockNumber: 3, TxHash: common.HexToHash("0xbb"), Index: 0, Topics: []common.Hash{common.HexToHash("0x02")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events)

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

	evs, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event named topic 0x01, got %d", len(evs))
	}
}

func TestIndexerIngestionIsIdempotentAcrossPolls(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 3; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(3)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 2, TxHash: common.HexToHash("0xaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Move the checkpoint back to block 1 (same chain, so no reorg) and poll again: blocks
	// 2..3 are scanned a second time and the log at block 2 is offered a second time
	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := source.HeaderByNumber(context.Background(), big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	cp.LastBlock, cp.LastBlockHash = 1, h1.Hash().Hex()
	if err := checkpoints.Set(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	callsBefore := source.FilterLogsCalls
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.FilterLogsCalls == callsBefore {
		t.Fatal("the second poll did not re-scan, so it proves nothing about idempotency")
	}

	all, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 event after repeated polling, got %d", len(all))
	}
}

func TestIndexerReorgRollsBackAndReingests(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 5; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(5)
	// old-chain logs: event "old-A" at block 4, event "old-B" at block 5
	source.AddLog(types.Log{Address: addr1, BlockNumber: 4, TxHash: common.HexToHash("0xa001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xaaaa")}})
	source.AddLog(types.Log{Address: addr1, BlockNumber: 5, TxHash: common.HexToHash("0xb001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xbbbb")}})

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	// use a small rollback window so the test needs only a few blocks
	idx := newTestIndexer(t, source, checkpoints, events, WithRollbackWindow(2))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	preReorg, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xaaaa").Hex())
	if len(preReorg) != 1 {
		t.Fatalf("expected old-A ingested before reorg, got %d", len(preReorg))
	}

	// Replace blocks 4/5 headers and old logs with fork event new-C at block 4
	source.SetHeader(4, 2)
	source.SetHeader(5, 2)
	source.RemoveLogsAtOrAfter(4)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 4, TxHash: common.HexToHash("0xc001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xcccc")}})
	source.SetHead(5)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll after reorg: %v", err)
	}

	// Checkpoint 5 minus rollback window 2 deletes from block 4: remove old-A/old-B and
	// retain new-C
	oldA, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xaaaa").Hex())
	if len(oldA) != 0 {
		t.Errorf("expected old-A to be rolled back, still present: %v", oldA)
	}
	oldB, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xbbbb").Hex())
	if len(oldB) != 0 {
		t.Errorf("expected old-B to be rolled back, still present: %v", oldB)
	}
	newC, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xcccc").Hex())
	if len(newC) != 1 {
		t.Fatalf("expected new-C to be re-ingested, got %d", len(newC))
	}

	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 5 {
		t.Errorf("LastBlock after reorg re-scan = %d, want 5", cp.LastBlock)
	}
}

func TestIndexerNoOpWhenNoNewBlocks(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1)
	source.SetHead(1)

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 1 {
		t.Errorf("LastBlock = %d, want 1", cp.LastBlock)
	}
}

// TestIndexerDeepReorgWalksBackPastFixedWindow checks full-fork recovery through
// retained hashes; fixed rollback would hide older stale events behind a re-baselined
// checkpoint
func TestIndexerDeepReorgWalksBackPastFixedWindow(t *testing.T) {
	source := NewFakeSource()
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil,
		WithRollbackWindow(3), WithBlockHashRepository(blockHashes), WithMaxRetainedHistory(256))

	// build a 20-block original chain, polling after every new block so every height
	// gets its own retained hash
	for i := uint64(1); i <= 20; i++ {
		source.SetHeader(i, 1)
		source.SetHead(i)
		if i == 15 {
			source.AddLog(types.Log{Address: addr1, BlockNumber: 15, TxHash: common.HexToHash("0xd001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xdddd")}})
		}
		if err := idx.Poll(context.Background()); err != nil {
			t.Fatalf("poll at block %d: %v", i, err)
		}
	}
	oldD, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xdddd").Hex())
	if len(oldD) != 1 {
		t.Fatalf("expected old-D ingested before reorg, got %d", len(oldD))
	}

	// Replace blocks 10..20, beyond rollbackWindow(3), which alone would leave old-D at
	// block 15
	for i := uint64(10); i <= 20; i++ {
		source.SetHeader(i, 2)
	}
	source.RemoveLogsAtOrAfter(10)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 12, TxHash: common.HexToHash("0xe001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xeeee")}})
	source.SetHead(20)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("poll after deep reorg: %v", err)
	}

	oldDAfter, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xdddd").Hex())
	if len(oldDAfter) != 0 {
		t.Errorf("expected old-D (block 15, inside the deep fork) to be rolled back; got %v", oldDAfter)
	}
	newE, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xeeee").Hex())
	if len(newE) != 1 {
		t.Fatalf("expected new-E to be re-ingested, got %d", len(newE))
	}

	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 20 {
		t.Errorf("LastBlock after deep reorg re-scan = %d, want 20", cp.LastBlock)
	}
}

// TestIndexerReorgBeyondRetainedHistoryRollsBackAndRebuildsFromTrustedStart checks
// rebuilding from the trusted start when it remains within maxAutoReorgDepth. No old
// events may survive unverified, including block 10 below the five-block retained
// window.
func TestIndexerReorgBeyondRetainedHistoryRollsBackAndRebuildsFromTrustedStart(t *testing.T) {
	source := NewFakeSource()
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil,
		WithRollbackWindow(2), WithBlockHashRepository(blockHashes), WithMaxRetainedHistory(5))

	for i := uint64(1); i <= 20; i++ {
		source.SetHeader(i, 1)
		source.SetHead(i)
		if i == 10 {
			// well below the retained floor (20-5=15)
			source.AddLog(types.Log{Address: addr1, BlockNumber: 10, TxHash: common.HexToHash("0xf001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xffff")}})
		}
		if i == 16 {
			source.AddLog(types.Log{Address: addr1, BlockNumber: 16, TxHash: common.HexToHash("0xa101"), Index: 0, Topics: []common.Hash{common.HexToHash("0xa1a1")}})
		}
		if err := idx.Poll(context.Background()); err != nil {
			t.Fatalf("poll at block %d: %v", i, err)
		}
	}

	// Replace the chain beyond maxHistory(5) but within maxAutoReorgDepth(100); remove all
	// old logs, leaving one new event at block 16
	for i := uint64(1); i <= 20; i++ {
		source.SetHeader(i, 2)
	}
	source.RemoveLogsAtOrAfter(1)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 16, TxHash: common.HexToHash("0xa102"), Index: 0, Topics: []common.Hash{common.HexToHash("0xa2a2")}})
	source.SetHead(20)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("poll after beyond-retained-history reorg: %v", err)
	}

	below, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xffff").Hex())
	if len(below) != 0 {
		t.Errorf("expected the orphaned below-retained-floor event (block 10) to be rolled back, got %d", len(below))
	}
	oldAbove, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xa1a1").Hex())
	if len(oldAbove) != 0 {
		t.Errorf("expected the orphaned above-retained-floor old event (block 16) to be rolled back, got %v", oldAbove)
	}
	newAbove, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xa2a2").Hex())
	if len(newAbove) != 1 {
		t.Fatalf("expected the new above-floor event to be re-ingested, got %d", len(newAbove))
	}

	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 20 {
		t.Errorf("LastBlock after rebuild-from-trusted-start = %d, want 20", cp.LastBlock)
	}
	if cp.ReconciliationRequired {
		t.Error("expected ReconciliationRequired = false: this reorg is within the automatic recovery policy")
	}
}

// TestIndexerReorgBeyondAutomaticPolicyEntersReconciliationRequired checks that
// excessive divergence preserves events and scan position, pauses with
// ErrReconciliationRequired, and requires operator reset before later polls resume
func TestIndexerReorgBeyondAutomaticPolicyEntersReconciliationRequired(t *testing.T) {
	source := NewFakeSource()
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil,
		WithBlockHashRepository(blockHashes), WithMaxRetainedHistory(50), WithMaxAutoReorgDepth(3))

	for i := uint64(1); i <= 20; i++ {
		source.SetHeader(i, 1)
		source.SetHead(i)
		if i == 10 {
			source.AddLog(types.Log{Address: addr1, BlockNumber: 10, TxHash: common.HexToHash("0xf001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xffff")}})
		}
		if err := idx.Poll(context.Background()); err != nil {
			t.Fatalf("poll at block %d: %v", i, err)
		}
	}

	// Replace blocks 10..20 beyond maxAutoReorgDepth(3), leaving no verified ancestor
	// within policy
	for i := uint64(10); i <= 20; i++ {
		source.SetHeader(i, 2)
	}
	source.RemoveLogsAtOrAfter(10)
	source.SetHead(20)

	err := idx.Poll(context.Background())
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("Poll after too-deep reorg: got %v, want ErrReconciliationRequired", err)
	}

	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if !cp.ReconciliationRequired {
		t.Error("expected ReconciliationRequired = true")
	}
	if cp.LastBlock != 20 {
		t.Errorf("checkpoint LastBlock changed during a refused rollback: got %d, want unchanged 20", cp.LastBlock)
	}

	// Keep unresolved events until operator recovery, neither trusting them as canonical
	// nor discarding them automatically
	stillThere, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xffff").Hex())
	if len(stillThere) != 1 {
		t.Fatalf("expected the unverified event to be left untouched pending reconciliation, got %d", len(stillThere))
	}

	// poll again: it must keep refusing, with no further chain reads or state
	// changes
	if err := idx.Poll(context.Background()); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("second Poll while flagged: got %v, want ErrReconciliationRequired", err)
	}
	cpAgain, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cpAgain.LastBlock != 20 || !cpAgain.ReconciliationRequired {
		t.Errorf("checkpoint changed on a second flagged Poll: %+v", cpAgain)
	}

	// the operator recovers by designating block 9 (the last block before the touched
	// range) as trusted
	if err := idx.ResetToTrustedCheckpoint(context.Background(), 9); err != nil {
		t.Fatalf("ResetToTrustedCheckpoint: %v", err)
	}
	afterReset, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if afterReset.ReconciliationRequired {
		t.Error("expected ReconciliationRequired cleared after ResetToTrustedCheckpoint")
	}
	if afterReset.LastBlock != 9 {
		t.Errorf("LastBlock after reset = %d, want 9", afterReset.LastBlock)
	}
	cleared, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xffff").Hex())
	if len(cleared) != 0 {
		t.Errorf("expected the orphaned event (block 10) to be rolled back by the reset, got %d", len(cleared))
	}

	// poll resumes normally, re-scanning forward from the trusted checkpoint
	source.AddLog(types.Log{Address: addr1, BlockNumber: 12, TxHash: common.HexToHash("0xe001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xeeee")}})
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll after reset: %v", err)
	}
	newE, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xeeee").Hex())
	if len(newE) != 1 {
		t.Fatalf("expected the new-chain event to be ingested after recovery, got %d", len(newE))
	}
	final, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if final.LastBlock != 20 || final.ReconciliationRequired {
		t.Errorf("expected normal resumed polling to reach LastBlock 20 and stay unflagged, got %+v", final)
	}
}

// TestIndexerHeadRegressionBelowCheckpointIsHandledExplicitly checks rollback below a
// regressed head instead of repeated missing-checkpoint-header failures
func TestIndexerHeadRegressionBelowCheckpointIsHandledExplicitly(t *testing.T) {
	source := NewFakeSource()
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil,
		WithBlockHashRepository(blockHashes), WithMaxRetainedHistory(50))

	for i := uint64(1); i <= 10; i++ {
		source.SetHeader(i, 1)
		source.SetHead(i)
		if i == 7 {
			source.AddLog(types.Log{Address: addr1, BlockNumber: 7, TxHash: common.HexToHash("0xb001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xbeef")}})
		}
		if err := idx.Poll(context.Background()); err != nil {
			t.Fatalf("poll at block %d: %v", i, err)
		}
	}

	// Regress head from 10 to 5 so headers 6..10 disappear, simulating RPC failover.
	// Fetching the old checkpoint header would repeatedly fail.
	source.SetHead(5)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll during head regression: %v", err)
	}

	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 5 {
		t.Errorf("LastBlock after head regression = %d, want 5", cp.LastBlock)
	}
	if cp.ReconciliationRequired {
		t.Error("expected ReconciliationRequired = false: block 5's hash was verified against retained history")
	}
	rolledBack, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xbeef").Hex())
	if len(rolledBack) != 0 {
		t.Errorf("expected the block-7 event to be rolled back after head regression to 5, got %d", len(rolledBack))
	}

	// Restore the same canonical chain; the next Poll must re-ingest its event
	// successfully
	source.SetHead(10)
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll after head recovers: %v", err)
	}
	reingested, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xbeef").Hex())
	if len(reingested) != 1 {
		t.Fatalf("expected the event to be re-ingested once the head recovers, got %d", len(reingested))
	}
}

// TestIndexerReplayAfterRollbackStaysIdempotent checks repeated polls after
// rollback/rescan without duplicate events
func TestIndexerReplayAfterRollbackStaysIdempotent(t *testing.T) {
	source := NewFakeSource()
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, nil,
		WithBlockHashRepository(blockHashes), WithMaxRetainedHistory(50))

	for i := uint64(1); i <= 5; i++ {
		source.SetHeader(i, 1)
		source.SetHead(i)
		if err := idx.Poll(context.Background()); err != nil {
			t.Fatalf("poll at block %d: %v", i, err)
		}
	}
	source.AddLog(types.Log{Address: addr1, BlockNumber: 4, TxHash: common.HexToHash("0xaa01"), Index: 0, Topics: []common.Hash{common.HexToHash("0xaaaa")}})

	// shallow reorg replacing blocks 4-5
	source.SetHeader(4, 2)
	source.SetHeader(5, 2)
	source.SetHead(5)
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("poll after reorg: %v", err)
	}
	first, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xaaaa").Hex())
	if len(first) != 1 {
		t.Fatalf("expected event ingested once after rollback+replay, got %d", len(first))
	}

	// poll repeatedly with nothing new: it must never duplicate
	for i := 0; i < 3; i++ {
		if err := idx.Poll(context.Background()); err != nil {
			t.Fatalf("idle poll %d: %v", i, err)
		}
	}
	again, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xaaaa").Hex())
	if len(again) != 1 {
		t.Errorf("expected still exactly 1 event after repeated idle polling, got %d", len(again))
	}
}

// --- Bounded log-range sync, DLQ ---

// TestIndexerScanLogsRestrictedRPCRange splits a 40-block backfill into FilterLogs
// calls of at most 10 blocks through WithMaxLogRange, satisfying the provider limit
// while ingesting every log
func TestIndexerScanLogsRestrictedRPCRange(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 40; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(40)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 3, TxHash: common.HexToHash("0xa1"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.AddLog(types.Log{Address: addr1, BlockNumber: 22, TxHash: common.HexToHash("0xa2"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.AddLog(types.Log{Address: addr1, BlockNumber: 39, TxHash: common.HexToHash("0xa3"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.MaxLogRangeBlocks = 10 // provider-side limit: reject anything wider

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events, WithMaxLogRange(10))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if source.FilterLogsCalls < 4 {
		t.Errorf("FilterLogsCalls = %d, want at least 4 chunk requests for a 40-block range at width 10", source.FilterLogsCalls)
	}
	evs, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("expected all 3 logs ingested across chunk boundaries, got %d", len(evs))
	}
	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil || cp.LastBlock != 40 {
		t.Fatalf("checkpoint = %+v, err %v; want LastBlock 40", cp, err)
	}
}

// TestIndexerScanLogsShrinksOnOversizedResponse checks adaptation to provider limits
// smaller than the configured maximum
func TestIndexerScanLogsShrinksOnOversizedResponse(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 20; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(20)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 15, TxHash: common.HexToHash("0xb1"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.MaxLogRangeBlocks = 4 // much smaller than the indexer's own default (2000)

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events)

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	evs, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected the log ingested after shrinking to fit the provider's limit, got %d", len(evs))
	}
}

// TestIndexerScanLogsRetriesTransientFailure requires bounded-backoff retries for
// transient FilterLogs failures that are not range errors
func TestIndexerScanLogsRetriesTransientFailure(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1)
	source.SetHead(1)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: common.HexToHash("0xc1"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.FilterLogsErrors = []error{ErrTransient, ErrTransient, nil}

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events, WithMaxRangeRetries(5), withSleepFunc(noSleep))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	evs, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if len(evs) != 1 {
		t.Fatalf("expected the log ingested after retrying past 2 transient failures, got %d", len(evs))
	}
}

// TestIndexerScanLogsGivesUpAndPreservesCheckpoint checks that exhausted retries leave
// progress unchanged; recovery resumes missing chunks without duplicating committed
// work
func TestIndexerScanLogsGivesUpAndPreservesCheckpoint(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 5; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(5)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 3, TxHash: common.HexToHash("0xd1"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.FilterLogsErrors = []error{ErrTransient, ErrTransient, ErrTransient}

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events, WithMaxRangeRetries(3), withSleepFunc(noSleep))

	if err := idx.Poll(context.Background()); err == nil {
		t.Fatal("expected Poll to surface the exhausted-retries error")
	}
	if _, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress); err == nil {
		t.Fatal("expected no checkpoint to have been persisted after a failed initial sync")
	}

	// the provider recovers: a fresh Poll must succeed and ingest the log that was
	// never seen
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll after recovery: %v", err)
	}
	evs, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event after resuming past the failure, got %d", len(evs))
	}
}

// TestIndexerDeadLetterQueueRecordsAndContinues checks that a bad log enters the DLQ
// while good logs and checkpoint progress persist
func TestIndexerDeadLetterQueueRecordsAndContinues(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1)
	source.SetHead(1)
	badTx := common.HexToHash("0xbad0")
	goodTx := common.HexToHash("0x9001")
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: badTx, Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: goodTx, Index: 1, Topics: []common.Hash{common.HexToHash("0x02")}})

	decode := func(log types.Log) (string, map[string]any, error) {
		if log.TxHash == badTx {
			return "", nil, errors.New("simulated decode failure: unknown ABI shape")
		}
		return "known", map[string]any{}, nil
	}

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	dlq := memory.NewDeadLetterRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, decode, WithDeadLetterQueue(dlq))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("expected Poll to succeed despite one unprocessable log, got %v", err)
	}

	good, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), "known")
	if len(good) != 1 {
		t.Fatalf("expected the good log ingested despite the bad one, got %d", len(good))
	}
	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil || cp.LastBlock != 1 {
		t.Fatalf("expected the checkpoint to still advance past the DLQ'd log, got %+v err %v", cp, err)
	}

	entries, err := dlq.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", len(entries))
	}
	if entries[0].TxHash != badTx.Hex() || entries[0].ErrorCategory != "decode" || entries[0].Resolved {
		t.Fatalf("unexpected DLQ entry: %+v", entries[0])
	}
}

// TestIndexerDeadLetterQueueRequiredToAvoidAbortingPoll checks the fallback
// without a DLQ: a decode failure aborts Poll instead of skipping the event
func TestIndexerDeadLetterQueueRequiredToAvoidAbortingPoll(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1)
	source.SetHead(1)
	badTx := common.HexToHash("0xbad0")
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: badTx, Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})
	decode := func(log types.Log) (string, map[string]any, error) {
		return "", nil, errors.New("simulated decode failure")
	}

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, decode)

	if err := idx.Poll(context.Background()); err == nil {
		t.Fatal("expected Poll to abort on a decode failure when no DLQ is configured")
	}
}

// TestIndexerRetryDeadLetterSucceeds checks retained-log ingestion and Resolved after
// the decode problem is fixed
func TestIndexerRetryDeadLetterSucceeds(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1)
	source.SetHead(1)
	badTx := common.HexToHash("0xbad0")
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: badTx, Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})

	decodeShouldFail := true
	decode := func(log types.Log) (string, map[string]any, error) {
		if decodeShouldFail {
			return "", nil, errors.New("simulated decode failure")
		}
		return "recovered", map[string]any{}, nil
	}

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	dlq := memory.NewDeadLetterRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, decode, WithDeadLetterQueue(dlq))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := dlq.List(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d err %v", len(entries), err)
	}
	id := entries[0].ID

	decodeShouldFail = false
	if err := idx.RetryDeadLetter(context.Background(), id); err != nil {
		t.Fatalf("RetryDeadLetter: %v", err)
	}

	recovered, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), "recovered")
	if len(recovered) != 1 {
		t.Fatalf("expected the retried log ingested, got %d", len(recovered))
	}
	entry, err := dlq.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Resolved {
		t.Fatal("expected the DLQ entry to be marked Resolved after a successful retry")
	}
}

// TestIndexerRetryDeadLetterDismissesOrphanedLog checks that a reorged entry resolves
// without ingestion
func TestIndexerRetryDeadLetterDismissesOrphanedLog(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1) // fork A
	source.SetHead(1)
	badTx := common.HexToHash("0xbad0")
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: badTx, Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})

	decode := func(log types.Log) (string, map[string]any, error) {
		return "", nil, errors.New("simulated decode failure")
	}

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	dlq := memory.NewDeadLetterRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 1, decode, WithDeadLetterQueue(dlq))

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := dlq.List(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d err %v", len(entries), err)
	}
	id := entries[0].ID

	// Reorganize block 1 to fork B before retry so its retained fork-A hash no longer
	// matches
	source.SetHeader(1, 2)

	if err := idx.RetryDeadLetter(context.Background(), id); err != nil {
		t.Fatalf("RetryDeadLetter: %v", err)
	}

	evs, _ := events.ListByName(context.Background(), chainID, addr1.Hex(), "recovered")
	if len(evs) != 0 {
		t.Fatalf("expected the orphaned log NOT ingested, got %d events", len(evs))
	}
	entry, err := dlq.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Resolved {
		t.Fatal("expected the orphaned entry to be marked Resolved (dismissed)")
	}
	if entry.ErrorCategory != models.DLQErrorOrphaned {
		t.Fatalf("ErrorCategory = %q, want %q", entry.ErrorCategory, models.DLQErrorOrphaned)
	}
}

// --- Reorg racing a single Poll call ---

// TestIndexerRejectsForkThatChangesBetweenFilterLogsAndHeaderByNumber switches to fork
// B after fork-A logs return. Events, retained hashes and checkpoint must all belong to
// B, avoiding hidden stale-fork data.
func TestIndexerRejectsForkThatChangesBetweenFilterLogsAndHeaderByNumber(t *testing.T) {
	source := NewFakeSource()
	for i := uint64(1); i <= 5; i++ {
		source.SetHeader(i, 1) // fork A everywhere initially
	}
	source.SetHead(2)

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	blockHashes := memory.NewBlockHashRepository()
	idx := newTestIndexer(t, source, checkpoints, events, WithBlockHashRepository(blockHashes), WithMaxRangeRetries(5), withSleepFunc(noSleep))

	// the first poll establishes a clean baseline checkpoint at block 2, fork A
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("initial Poll: %v", err)
	}

	// Fork A's block-3 event would slip through without post-scan validation
	forkAEvent := common.HexToHash("0xA000")
	source.AddLog(types.Log{Address: addr1, BlockNumber: 3, TxHash: common.HexToHash("0xa3"), Index: 0, Topics: []common.Hash{forkAEvent}})
	source.SetHead(5)

	reorgFired := false
	source.FilterLogsHook = func() {
		reorgFired = true
		// Switch to fork B after FilterLogs returns A's logs, before the next HeaderByNumber
		for i := uint64(3); i <= 5; i++ {
			source.SetHeader(i, 2)
		}
		source.RemoveLogsAtOrAfter(3) // fork A's log disappears with its fork
		source.AddLog(types.Log{Address: addr1, BlockNumber: 3, TxHash: common.HexToHash("0xb3"), Index: 0, Topics: []common.Hash{common.HexToHash("0xB000")}})
	}

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll racing a reorg: %v", err)
	}
	if !reorgFired {
		t.Fatal("test setup error: FilterLogsHook never fired")
	}

	// no fork-A event may have survived, under any checkpoint
	forkA, err := events.ListByName(context.Background(), chainID, addr1.Hex(), forkAEvent.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(forkA) != 0 {
		t.Fatalf("expected zero fork-A events to survive, got %d", len(forkA))
	}

	// fork B's replacement event must be the one indexed
	forkB, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0xB000").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(forkB) != 1 {
		t.Fatalf("expected exactly 1 fork-B event ingested, got %d", len(forkB))
	}

	// Checkpoint 5 must use fork B's hash, not the pre-reorg observation
	cp, err := checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := source.HeaderByNumber(context.Background(), new(big.Int).SetUint64(5))
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlockHash != wantHash.Hash().Hex() {
		t.Fatalf("checkpoint hash = %s, want fork B's actual hash %s", cp.LastBlockHash, wantHash.Hash().Hex())
	}

	// Retained hashes must all describe fork B for later ancestor searches
	for i := uint64(3); i <= 5; i++ {
		got, err := blockHashes.Get(context.Background(), chainID, i)
		if err != nil {
			t.Fatalf("block hash at %d: %v", i, err)
		}
		header, err := source.HeaderByNumber(context.Background(), new(big.Int).SetUint64(i))
		if err != nil {
			t.Fatal(err)
		}
		if got != header.Hash().Hex() {
			t.Fatalf("retained hash at %d = %s, want fork B's %s", i, got, header.Hash().Hex())
		}
	}
}

// TestIndexerRetriesWhenLogBlockHashDisagreesWithCanonicalHeader changes an interior
// block while its boundary stays fixed, requiring per-log hash validation
func TestIndexerRetriesWhenLogBlockHashDisagreesWithCanonicalHeader(t *testing.T) {
	source := NewFakeSource()
	source.SetHeader(1, 1)
	source.SetHeader(2, 1)
	source.SetHead(2)

	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := newTestIndexer(t, source, checkpoints, events, WithMaxRangeRetries(5), withSleepFunc(noSleep))

	// Place the event inside block 1, below chunk boundary 2
	source.AddLog(types.Log{Address: addr1, BlockNumber: 1, TxHash: common.HexToHash("0xaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")}})

	swapped := false
	source.FilterLogsHook = func() {
		if swapped {
			return
		}
		swapped = true
		// Change only block 1; a boundary-only check at block 2 would miss it
		source.SetHeader(1, 2)
	}

	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !swapped {
		t.Fatal("test setup error: hook never fired")
	}
	// Commit the log only after retry returns its current canonical hash, never on the
	// first stale observation
	evs, err := events.ListByName(context.Background(), chainID, addr1.Hex(), common.HexToHash("0x01").Hex())
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 event after the retry recovers, got %d", len(evs))
	}
}

// With startBlock 0 and depth below maxAutoReorgDepth, both recovery floors are 0;
// a full-chain replacement must rebuild from genesis without ReconciliationRequired
func TestIndexerShallowChainFullReplacementRebuildsAutomaticallyFromGenesis(t *testing.T) {
	ctx := context.Background()
	source := NewFakeSource()
	checkpoints := memory.NewIndexerCheckpointRepository()
	events := memory.NewChainEventRepository()
	idx := New(source, checkpoints, events, chainID, []common.Address{addr1}, 0, nil,
		WithBlockHashRepository(memory.NewBlockHashRepository()), WithMaxAutoReorgDepth(100))

	source.SetHeader(0, 1)
	for i := uint64(1); i <= 10; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(10)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 3, TxHash: common.HexToHash("0xa001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xaaaa")}})
	if err := idx.Poll(ctx); err != nil {
		t.Fatalf("first poll: %v", err)
	}

	// every block of the old chain is replaced, including the checkpoint block
	for i := uint64(1); i <= 10; i++ {
		source.SetHeader(i, 2)
	}
	source.RemoveLogsAtOrAfter(0)
	source.AddLog(types.Log{Address: addr1, BlockNumber: 4, TxHash: common.HexToHash("0xb001"), Index: 0, Topics: []common.Hash{common.HexToHash("0xbbbb")}})
	source.SetHead(11)
	source.SetHeader(11, 2)

	if err := idx.Poll(ctx); err != nil {
		if errors.Is(err, ErrReconciliationRequired) {
			t.Fatalf("a shallow fork with startBlock 0 must rebuild automatically, got %v", err)
		}
		t.Fatalf("poll after reorg: %v", err)
	}

	if old, _ := events.ListByName(ctx, chainID, addr1.Hex(), common.HexToHash("0xaaaa").Hex()); len(old) != 0 {
		t.Errorf("the orphaned event survived the rebuild: %v", old)
	}
	if fresh, _ := events.ListByName(ctx, chainID, addr1.Hex(), common.HexToHash("0xbbbb").Hex()); len(fresh) != 1 {
		t.Errorf("new-chain events after rebuild = %d, want 1", len(fresh))
	}
	cp, err := checkpoints.Get(ctx, chainID, CheckpointAddress)
	if err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 11 || cp.ReconciliationRequired {
		t.Errorf("checkpoint = block %d, reconciliationRequired %v; want block 11 and false", cp.LastBlock, cp.ReconciliationRequired)
	}
}
