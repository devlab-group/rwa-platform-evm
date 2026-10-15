package indexer

import (
	"context"
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// FakeSource models headers and logs in memory. Tests simulate reorgs by changing
// SetHeader seeds and replacing logs at affected heights.
type FakeSource struct {
	mu      sync.Mutex
	headers map[uint64]*types.Header
	logs    []types.Log
	head    uint64

	// FilterLogsErrors supplies FIFO per-call failures without networking; nil entries
	// allow successful calls between faults
	FilterLogsErrors []error
	// MaxLogRangeBlocks rejects wider FilterLogs ranges when positive, independently of
	// the one-shot error queue
	MaxLogRangeBlocks uint64
	// FilterLogsCalls counts FilterLogs invocations, so a test can assert how many
	// chunk requests a scan issued
	FilterLogsCalls int
	// FilterLogsHook runs once after response computation, before return, allowing tests
	// to simulate a reorg before HeaderByNumber without goroutines
	FilterLogsHook func()
}

// ErrRangeTooLarge is a canned error that isRangeTooLargeError matches, for tests
// of the shrink-on-oversized-response path
var ErrRangeTooLarge = errors.New("query returned more than 10000 results")

// ErrTransient is a canned error that isRangeTooLargeError and isTimeoutError do
// not match, for tests of the backoff retry path
var ErrTransient = errors.New("simulated transient RPC failure")

// NewFakeSource returns an empty FakeSource. Build chain state with SetHeader,
// AddLog and SetHead.
func NewFakeSource() *FakeSource {
	return &FakeSource{headers: map[uint64]*types.Header{}}
}

// SetHeader sets the header at number, replacing any existing one to simulate a
// reorg. seed varies the header's hash deterministically.
func (f *FakeSource) SetHeader(number uint64, seed byte) *types.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := &types.Header{Number: new(big.Int).SetUint64(number), Extra: []byte{seed}}
	f.headers[number] = h
	return h
}

// SetHead sets the current chain head block number.
func (f *FakeSource) SetHead(number uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head = number
}

// AddLog appends a log to the fake chain's flat log list.
func (f *FakeSource) AddLog(l types.Log) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, l)
}

// Logs copies the complete current log list after additions/removals, without
// FilterLogs selection. internal/shadow uses it for independent expected counts.
func (f *FakeSource) Logs() []types.Log {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.Log, len(f.logs))
	copy(out, f.logs)
	return out
}

// RemoveLogsAtOrAfter drops logs from blockNumber onward; AddLog can supply
// replacement-fork logs
func (f *FakeSource) RemoveLogsAtOrAfter(blockNumber uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.logs[:0]
	for _, l := range f.logs {
		if l.BlockNumber < blockNumber {
			kept = append(kept, l)
		}
	}
	f.logs = kept
}

func (f *FakeSource) BlockNumber(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}

func (f *FakeSource) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.headers[number.Uint64()]
	if !ok {
		return nil, errors.New("indexer: fake source has no header at that height")
	}
	return h, nil
}

func (f *FakeSource) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.mu.Lock()
	f.FilterLogsCalls++
	from := uint64(0)
	if q.FromBlock != nil {
		from = q.FromBlock.Uint64()
	}
	to := ^uint64(0)
	if q.ToBlock != nil {
		to = q.ToBlock.Uint64()
	}
	if len(f.FilterLogsErrors) > 0 {
		err := f.FilterLogsErrors[0]
		f.FilterLogsErrors = f.FilterLogsErrors[1:]
		if err != nil {
			f.mu.Unlock()
			return nil, err
		}
	}
	if f.MaxLogRangeBlocks > 0 && to != ^uint64(0) && to-from+1 > f.MaxLogRangeBlocks {
		f.mu.Unlock()
		return nil, ErrRangeTooLarge
	}
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber < from || l.BlockNumber > to {
			continue
		}
		if len(q.Addresses) > 0 {
			match := false
			for _, a := range q.Addresses {
				if a == l.Address {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		// Stamp each log from its current header, avoiding manual synchronization after
		// SetHeader. Changing a header between FilterLogs calls then returns old/new
		// BlockHashes for validation tests; see
		// TestIndexerRejectsForkThatChangesBetweenFilterLogsAndHeaderByNumber.
		stamped := l
		if h, ok := f.headers[l.BlockNumber]; ok {
			stamped.BlockHash = h.Hash()
		}
		out = append(out, stamped)
	}
	hook := f.FilterLogsHook
	f.FilterLogsHook = nil // one-shot
	f.mu.Unlock()
	if hook != nil {
		// Run the one-shot hook after response computation, outside the lock so
		// SetHeader/AddLog cannot deadlock
		hook()
	}
	return out, nil
}

var _ ChainSource = (*FakeSource)(nil)
