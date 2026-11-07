package indexer

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

var (
	watchedAddr = common.HexToAddress("0x00000000000000000000000000000000000000A0")
	addedAddr   = common.HexToAddress("0x00000000000000000000000000000000000000B0")
	bothAddrs   = []common.Address{watchedAddr, addedAddr}
	onlyWatched = []common.Address{watchedAddr}
)

// recordingSource records FilterLogs requests so tests can check scan ranges and addresses
type recordingSource struct {
	*FakeSource
	queries []ethereum.FilterQuery
}

func (s *recordingSource) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	s.queries = append(s.queries, q)
	return s.FakeSource.FilterLogs(ctx, q)
}

// chain creates headers through blocks and sets the initial head; SetHead advances it later
func chain(blocks, head uint64) *recordingSource {
	source := NewFakeSource()
	for i := uint64(1); i <= blocks; i++ {
		source.SetHeader(i, 1)
	}
	source.SetHead(head)
	return &recordingSource{FakeSource: source}
}

func logAt(addr common.Address, block uint64) types.Log {
	return types.Log{
		Address:     addr,
		BlockNumber: block,
		Topics:      []common.Hash{common.HexToHash("0x01")},
		TxHash:      common.BytesToHash(append(addr.Bytes(), byte(block))),
	}
}

// stores are the indexer's repositories; opts selects its write path
type stores struct {
	checkpoints *memory.IndexerCheckpointRepository
	events      *memory.ChainEventRepository
	hashes      *memory.BlockHashRepository
	opts        []Option
}

func newStores(atomic bool) *stores {
	s := &stores{
		checkpoints: memory.NewIndexerCheckpointRepository(),
		events:      memory.NewChainEventRepository(),
		hashes:      memory.NewBlockHashRepository(),
		opts:        []Option{withSleepFunc(noSleep), WithMaxRangeRetries(1)},
	}
	s.opts = append(s.opts, WithBlockHashRepository(s.hashes))
	if atomic {
		s.opts = append(s.opts, WithChunkCommitter(memory.NewChainChunkRepository(s.events, s.hashes, s.checkpoints)))
	}
	return s
}

func (s *stores) indexer(source ChainSource, addresses []common.Address, startBlock uint64, opts ...Option) *Indexer {
	return New(source, s.checkpoints, s.events, chainID, addresses, startBlock, nil, append(slices.Clone(s.opts), opts...)...)
}

func (s *stores) checkpoint(t *testing.T) *models.IndexerCheckpoint {
	t.Helper()
	cp, err := s.checkpoints.Get(context.Background(), chainID, CheckpointAddress)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return cp
}

func (s *stores) wantBlocks(t *testing.T, addr common.Address, want ...uint64) {
	t.Helper()
	all, err := s.events.ListAll(context.Background(), chainID)
	if err != nil {
		t.Fatal(err)
	}
	got := []uint64{}
	for _, e := range all {
		if e.Address == addr.Hex() {
			got = append(got, e.BlockNumber)
		}
	}
	if !slices.Equal(got, append([]uint64{}, want...)) {
		t.Errorf("stored blocks of %s = %v, want %v", addr.Hex(), got, want)
	}
}

func (s *stores) wantWatched(t *testing.T, want ...common.Address) {
	t.Helper()
	wantHex := []string{}
	for _, a := range want {
		wantHex = append(wantHex, a.Hex())
	}
	if got := s.checkpoint(t).Watched; !slices.Equal(append([]string{}, got...), wantHex) {
		t.Errorf("Watched = %v, want %v", got, wantHex)
	}
}

func poll(t *testing.T, idx *Indexer) {
	t.Helper()
	if err := idx.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
}

// scan is one FilterLogs request in comparable form
type scan struct {
	from, to  uint64
	addresses string
}

func scans(queries []ethereum.FilterQuery) []scan {
	out := []scan{}
	for _, q := range queries {
		addrs := make([]string, 0, len(q.Addresses))
		for _, a := range q.Addresses {
			addrs = append(addrs, a.Hex())
		}
		out = append(out, scan{q.FromBlock.Uint64(), q.ToBlock.Uint64(), strings.Join(addrs, ",")})
	}
	return out
}

func wantScans(t *testing.T, source *recordingSource, want ...scan) {
	t.Helper()
	got := scans(source.queries)
	if !slices.Equal(got, append([]scan{}, want...)) {
		t.Errorf("scans = %+v, want %+v", got, want)
	}
	source.queries = nil
}

func forEachWritePath(t *testing.T, fn func(t *testing.T, s *stores)) {
	t.Helper()
	for name, atomic := range map[string]bool{"separate writes": false, "atomic commits": true} {
		t.Run(name, func(t *testing.T) { fn(t, newStores(atomic)) })
	}
}

// After activation or a strategy swap, the rewired indexer must backfill new addresses
// below the existing checkpoint
func TestPoll_AddressAddedBelowCheckpoint_StoresItsEarlierLogs(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(20, 10)
		source.AddLog(logAt(addedAddr, 5))
		poll(t, s.indexer(source, onlyWatched, 1))

		source.SetHead(20)
		source.AddLog(logAt(addedAddr, 15))
		poll(t, s.indexer(source, bothAddrs, 1))

		s.wantBlocks(t, addedAddr, 5, 15)
		s.wantWatched(t, bothAddrs...)
		if cp := s.checkpoint(t); cp.LastBlock != 20 {
			t.Errorf("LastBlock = %d, want 20", cp.LastBlock)
		}
	})
}

func TestPoll_Backfill_ScansOnlyTheAddedAddressFromStartBlockThroughCheckpoint(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(20, 10)
		for _, block := range []uint64{2, 3, 10} {
			source.AddLog(logAt(addedAddr, block))
		}
		poll(t, s.indexer(source, onlyWatched, 3))
		wantScans(t, source, scan{3, 10, watchedAddr.Hex()})

		source.SetHead(20)
		poll(t, s.indexer(source, bothAddrs, 3))

		wantScans(t, source,
			scan{3, 10, addedAddr.Hex()},
			scan{11, 20, watchedAddr.Hex() + "," + addedAddr.Hex()},
		)
		s.wantBlocks(t, addedAddr, 3, 10) // block 2 lies before the start block
	})
}

func TestPoll_SameAddresses_ScanOnlyForward(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(20, 10)
		idx := s.indexer(source, bothAddrs, 1)
		poll(t, idx)
		s.wantWatched(t, bothAddrs...)
		source.queries = nil

		source.SetHead(20)
		poll(t, idx)
		poll(t, s.indexer(source, bothAddrs, 1)) // a restart changes nothing

		wantScans(t, source, scan{11, 20, watchedAddr.Hex() + "," + addedAddr.Hex()})
	})
}

// A checkpoint without Watched triggers a one-time backfill of every watched address;
// stored logs remain unchanged
func TestPoll_CheckpointWithoutWatched_BackfillsEveryAddressOnce(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		ctx := context.Background()
		source := chain(10, 10)
		source.AddLog(logAt(watchedAddr, 5))
		source.AddLog(logAt(watchedAddr, 6))
		h, _ := source.HeaderByNumber(ctx, big.NewInt(10))
		if err := s.checkpoints.Set(ctx, &models.IndexerCheckpoint{
			ChainID:       chainID,
			Address:       CheckpointAddress,
			LastBlock:     10,
			LastBlockHash: h.Hash().Hex(),
		}); err != nil {
			t.Fatal(err)
		}
		stored := logAt(watchedAddr, 5)
		if err := s.events.Create(ctx, &models.ChainEvent{
			ChainID:     chainID,
			Address:     watchedAddr.Hex(),
			TxHash:      stored.TxHash.Hex(),
			BlockNumber: 5,
			Name:        "kept",
		}); err != nil {
			t.Fatal(err)
		}

		idx := s.indexer(source, onlyWatched, 1)
		poll(t, idx)
		poll(t, idx)

		wantScans(t, source, scan{1, 10, watchedAddr.Hex()})
		s.wantBlocks(t, watchedAddr, 5, 6)
		kept, err := s.events.ListByName(ctx, chainID, watchedAddr.Hex(), "kept")
		if err != nil || len(kept) != 1 {
			t.Errorf("stored event rewritten: %d rows, err %v", len(kept), err)
		}
	})
}

// Anvil mines only on demand, so the chain may sit still when the address set changes
func TestPoll_NoNewBlocks_StillBackfills(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(10, 10)
		source.AddLog(logAt(addedAddr, 5))
		poll(t, s.indexer(source, onlyWatched, 1))

		poll(t, s.indexer(source, bothAddrs, 1))

		s.wantBlocks(t, addedAddr, 5)
		s.wantWatched(t, bothAddrs...)
	})
}

func TestPoll_NoNewBlocks_ReportsBackfillFailure(t *testing.T) {
	s := newStores(false)
	source := chain(10, 10)
	poll(t, s.indexer(source, onlyWatched, 1))

	source.FilterLogsErrors = []error{ErrTransient}
	err := s.indexer(source, bothAddrs, 1).Poll(context.Background())

	if !errors.Is(err, ErrTransient) {
		t.Fatalf("Poll = %v, want the backfill failure", err)
	}
	s.wantWatched(t, watchedAddr)
}

func TestPoll_BackfillFails_LiveIndexingContinuesAndTheNextPollResumes(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(24, 12)
		for _, block := range []uint64{2, 7, 11, 15} {
			source.AddLog(logAt(addedAddr, block))
		}
		source.AddLog(logAt(watchedAddr, 15))
		poll(t, s.indexer(source, onlyWatched, 1))
		source.queries = nil

		source.SetHead(20)
		source.FilterLogsErrors = []error{nil, ErrTransient}
		idx := s.indexer(source, bothAddrs, 1, WithMaxLogRange(4))
		err := idx.Poll(context.Background())

		if !errors.Is(err, ErrTransient) {
			t.Fatalf("Poll = %v, want the backfill failure", err)
		}
		s.wantBlocks(t, watchedAddr, 15) // the forward scan still ran
		s.wantBlocks(t, addedAddr, 2, 15)
		s.wantWatched(t, watchedAddr) // the missing range must not read as covered
		if cp := s.checkpoint(t); cp.LastBlock != 20 {
			t.Errorf("LastBlock = %d, want 20", cp.LastBlock)
		}
		source.queries = nil

		poll(t, idx)

		s.wantBlocks(t, addedAddr, 2, 7, 11, 15)
		s.wantWatched(t, bothAddrs...)
		if got := scans(source.queries)[0]; got != (scan{5, 8, addedAddr.Hex()}) {
			t.Errorf("first scan after the failure = %+v, want the failed chunk", got)
		}
	})
}

func TestPoll_RestartMidBackfill_StartsAgainAndStoresEachLogOnce(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(12, 12)
		for _, block := range []uint64{2, 7} {
			source.AddLog(logAt(addedAddr, block))
		}
		poll(t, s.indexer(source, onlyWatched, 1))

		source.FilterLogsErrors = []error{nil, ErrTransient}
		if err := s.indexer(source, bothAddrs, 1, WithMaxLogRange(4)).Poll(context.Background()); !errors.Is(err, ErrTransient) {
			t.Fatalf("Poll = %v, want the backfill failure", err)
		}
		source.queries = nil

		poll(t, s.indexer(source, bothAddrs, 1, WithMaxLogRange(4)))

		s.wantBlocks(t, addedAddr, 2, 7)
		s.wantWatched(t, bothAddrs...)
		if got := scans(source.queries)[0]; got != (scan{1, 4, addedAddr.Hex()}) {
			t.Errorf("first scan after restart = %+v, want the start block", got)
		}
	})
}

func TestPoll_BackfillRacesReorg_StoresNoForkLog(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(10, 10)
		source.AddLog(logAt(addedAddr, 5))
		poll(t, s.indexer(source, onlyWatched, 1))

		source.FilterLogsHook = func() { source.SetHeader(5, 2) } // block 5 forks after the scan answered
		idx := s.indexer(source, bothAddrs, 1)
		err := idx.Poll(context.Background())

		if err == nil || !strings.Contains(err.Error(), "raced a reorg") {
			t.Fatalf("Poll = %v, want the reorg race reported", err)
		}
		s.wantBlocks(t, addedAddr)
		s.wantWatched(t, watchedAddr)

		poll(t, idx)

		s.wantBlocks(t, addedAddr, 5)
		s.wantWatched(t, bothAddrs...)
	})
}

// A rollback rewinds below a finished backfill whose commit lost a race: the forward scan
// must replay the rolled-back range for the added address too
func TestPoll_RollbackBelowBackfilledRange_ReplaysItFromTheFork(t *testing.T) {
	s := newStores(true)
	committer := &loseOneCommit{ChainChunkRepository: memory.NewChainChunkRepository(s.events, s.hashes, s.checkpoints)}
	s.opts = append(s.opts, WithChunkCommitter(committer))
	source := chain(12, 10)
	source.AddLog(logAt(addedAddr, 5))
	source.AddLog(logAt(addedAddr, 9))
	poll(t, s.indexer(source, onlyWatched, 1))

	committer.lose = true
	idx := s.indexer(source, bothAddrs, 1)
	poll(t, idx) // the backfill ran and its watched-set commit lost
	s.wantBlocks(t, addedAddr, 5, 9)
	s.wantWatched(t, watchedAddr)

	source.RemoveLogsAtOrAfter(8)
	forkTo(source.FakeSource, 8, 12, 2)
	source.SetHead(12)
	source.AddLog(logAt(addedAddr, 11))
	poll(t, idx)

	s.wantBlocks(t, addedAddr, 5, 11) // block 9 was on the abandoned fork
	s.wantWatched(t, bothAddrs...)
	if cp := s.checkpoint(t); cp.LastBlock != 12 {
		t.Errorf("LastBlock = %d, want 12", cp.LastBlock)
	}
}

// loseOneCommit makes the next CommitChunk lose the version race when lose is set
type loseOneCommit struct {
	*memory.ChainChunkRepository
	lose bool
}

func (c *loseOneCommit) CommitChunk(ctx context.Context, cc repository.ChunkCommit) (bool, error) {
	if c.lose {
		c.lose = false
		return false, nil
	}
	return c.ChainChunkRepository.CommitChunk(ctx, cc)
}

func TestPoll_WatchedSetCommitLosesRace_NextPollFinishesWithoutRescanning(t *testing.T) {
	s := newStores(true)
	committer := &loseOneCommit{ChainChunkRepository: memory.NewChainChunkRepository(s.events, s.hashes, s.checkpoints)}
	s.opts = append(s.opts, WithChunkCommitter(committer))
	source := chain(10, 10)
	source.AddLog(logAt(addedAddr, 5))
	poll(t, s.indexer(source, onlyWatched, 1))
	before := s.checkpoint(t).Version
	source.queries = nil

	committer.lose = true
	idx := s.indexer(source, bothAddrs, 1)
	poll(t, idx)
	s.wantWatched(t, watchedAddr)
	poll(t, idx)

	wantScans(t, source, scan{1, 10, addedAddr.Hex()})
	s.wantWatched(t, bothAddrs...)
	if got := s.checkpoint(t).Version; got != before+1 {
		t.Errorf("Version = %d, want %d", got, before+1)
	}
}

// An empty FilterLogs address list matches every contract, so removing addresses must
// not trigger a backfill scan
func TestPoll_AddressRemoved_ScansNothingBelowTheCheckpoint(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(10, 10)
		poll(t, s.indexer(source, bothAddrs, 1))
		source.queries = nil

		poll(t, s.indexer(source, onlyWatched, 1))

		wantScans(t, source)
		s.wantWatched(t, watchedAddr)
	})
}

// A strategy swapped out and back was not scanned in between
func TestPoll_AddressRemovedThenAddedBack_StoresTheGap(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(20, 10)
		source.AddLog(logAt(addedAddr, 5))
		poll(t, s.indexer(source, bothAddrs, 1))

		source.SetHead(15)
		source.AddLog(logAt(addedAddr, 12))
		poll(t, s.indexer(source, onlyWatched, 1))
		s.wantBlocks(t, addedAddr, 5)

		poll(t, s.indexer(source, bothAddrs, 1))

		s.wantBlocks(t, addedAddr, 5, 12)
		s.wantWatched(t, bothAddrs...)
	})
}

func TestPoll_BackfillOverProviderRangeLimit_ShrinksAndCoversEveryBlock(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(20, 20)
		for block := uint64(1); block <= 20; block += 3 {
			source.AddLog(logAt(addedAddr, block))
		}
		poll(t, s.indexer(source, onlyWatched, 1))
		source.MaxLogRangeBlocks = 3

		poll(t, s.indexer(source, bothAddrs, 1, WithMaxRangeRetries(10)))

		s.wantBlocks(t, addedAddr, 1, 4, 7, 10, 13, 16, 19)
		s.wantWatched(t, bothAddrs...)
	})
}

// failingEvents refuses every write while fail is set
type failingEvents struct {
	repository.ChainEventRepository
	fail bool
}

var errStoreDown = errors.New("store down")

func (e *failingEvents) Create(ctx context.Context, ev *models.ChainEvent) error {
	if e.fail {
		return errStoreDown
	}
	return e.ChainEventRepository.Create(ctx, ev)
}

func TestPoll_BackfillCannotStoreALog_KeepsTheAddressUncovered(t *testing.T) {
	s := newStores(false)
	source := chain(10, 10)
	source.AddLog(logAt(addedAddr, 5))
	poll(t, s.indexer(source, onlyWatched, 1))

	events := &failingEvents{ChainEventRepository: s.events, fail: true}
	idx := New(source, s.checkpoints, events, chainID, bothAddrs, 1, nil, s.opts...)
	if err := idx.Poll(context.Background()); !errors.Is(err, errStoreDown) {
		t.Fatalf("Poll = %v, want the store failure", err)
	}
	s.wantBlocks(t, addedAddr)
	s.wantWatched(t, watchedAddr)

	events.fail = false
	poll(t, idx)

	s.wantBlocks(t, addedAddr, 5)
	s.wantWatched(t, bothAddrs...)
}

// The forward scan starts above the checkpoint, so only the backfill reads the checkpoint block
func TestPoll_BackfillLastChunkIsTheCheckpointBlock_StoresItsLog(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(9, 9)
		source.AddLog(logAt(addedAddr, 9))
		poll(t, s.indexer(source, onlyWatched, 1))

		poll(t, s.indexer(source, bothAddrs, 1, WithMaxLogRange(4)))

		s.wantBlocks(t, addedAddr, 9)
		s.wantWatched(t, bothAddrs...)
	})
}

func TestPoll_BackfillUndecodableLog_GoesToDeadLettersAndTheRestIsStored(t *testing.T) {
	s := newStores(true)
	dlq := memory.NewDeadLetterRepository()
	s.opts = append(s.opts, WithDeadLetterQueue(dlq))
	source := chain(10, 10)
	source.AddLog(logAt(addedAddr, 5))
	source.AddLog(logAt(addedAddr, 7))
	poll(t, s.indexer(source, onlyWatched, 1))

	decode := func(log types.Log) (string, map[string]any, error) {
		if log.BlockNumber == 5 {
			return "", nil, errors.New("bad log")
		}
		return "Fine", map[string]any{}, nil
	}
	poll(t, New(source, s.checkpoints, s.events, chainID, bothAddrs, 1, decode, s.opts...))

	s.wantBlocks(t, addedAddr, 7)
	s.wantWatched(t, bothAddrs...)
	entries, err := dlq.List(context.Background())
	if err != nil || len(entries) != 1 || entries[0].BlockNumber != 5 {
		t.Errorf("dead letters = %+v, err %v, want the log at block 5", entries, err)
	}
}

// A rewind that forgot Watched would hide a pending backfill or repeat a finished one
func TestWatched_SurvivesRollbackAndTrustedReset(t *testing.T) {
	forEachWritePath(t, func(t *testing.T, s *stores) {
		source := chain(14, 10)
		idx := s.indexer(source, bothAddrs, 1)
		poll(t, idx)
		source.queries = nil
		both := watchedAddr.Hex() + "," + addedAddr.Hex()

		forkTo(source.FakeSource, 8, 10, 2)
		poll(t, idx)
		wantScans(t, source, scan{8, 10, both})
		s.wantWatched(t, bothAddrs...)

		if err := idx.ResetToTrustedCheckpoint(context.Background(), 9); err != nil {
			t.Fatal(err)
		}
		s.wantWatched(t, bothAddrs...)

		source.SetHead(14)
		poll(t, idx)
		wantScans(t, source, scan{10, 14, both})
	})
}

// A reset with no stored checkpoint has no record of what was indexed below it
func TestResetToTrustedCheckpoint_NoCheckpoint_BackfillsBelowTheTrustedBlock(t *testing.T) {
	s := newStores(false)
	source := chain(10, 10)
	source.AddLog(logAt(addedAddr, 5))
	idx := s.indexer(source, bothAddrs, 1)

	if err := idx.ResetToTrustedCheckpoint(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	poll(t, idx)

	s.wantBlocks(t, addedAddr, 5)
	s.wantWatched(t, bothAddrs...)
}
