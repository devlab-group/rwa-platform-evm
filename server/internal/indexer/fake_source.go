package indexer

import (
	"context"
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// FakeSource is an in-memory ChainSource for tests. It models a linear chain of
// headers and a flat log list. A test simulates a reorg by replacing headers
// (SetHeader with a different seed gives a different hash) and changing which logs
// exist at those heights.
type FakeSource struct {
	mu      sync.Mutex
	headers map[uint64]*types.Header
	logs    []types.Log
	head    uint64

	// FilterLogsErrors, if non-empty, is consumed first in, first out, one entry per
	// FilterLogs call, and a non-nil entry is returned instead of a normal result.
	// This simulates a sequence of RPC failures (oversized response, timeout) without
	// network I/O. A nil entry makes that call succeed, so a fault can be placed at a
	// given position in a sequence of otherwise successful calls.
	FilterLogsErrors []error
	// MaxLogRangeBlocks, if greater than 0, makes FilterLogs return an oversized-range
	// error for any query spanning more blocks than this, simulating a provider's
	// range limit. It is independent of the one-shot FilterLogsErrors queue.
	MaxLogRangeBlocks uint64
	// FilterLogsCalls counts FilterLogs invocations, so a test can assert how many
	// chunk requests a scan issued
	FilterLogsCalls int
	// FilterLogsHook, if set, runs once and is then cleared. It runs after a
	// FilterLogs call has computed its response and before it returns, so a test can
	// change chain state at that point (typically SetHeader, simulating a reorg)
	// without real concurrency. This reproduces a node reorganizing between
	// FilterLogs and HeaderByNumber.
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

// Logs returns a copy of every log tracked so far (after all AddLog and
// RemoveLogsAtOrAfter calls), regardless of any FilterLogs filtering.
// internal/shadow uses it to compute an expected event count independently of
// the indexer.
func (f *FakeSource) Logs() []types.Log {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.Log, len(f.logs))
	copy(out, f.logs)
	return out
}

// RemoveLogsAtOrAfter drops every log at or after blockNumber, simulating
// the old chain's logs disappearing during a reorg before new ones are
// added back via AddLog.
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
		// stamp BlockHash from the header currently registered at the log's block number,
		// as a real node's eth_getLogs response reflects its canonical view at query
		// time. Tests need not keep AddLog's BlockHash in sync with SetHeader, and a
		// SetHeader between two FilterLogs calls yields a log with the old hash on the
		// first call and the new hash on the second. That is the race the validation
		// logic must handle (see
		// TestIndexerRejectsForkThatChangesBetweenFilterLogsAndHeaderByNumber)
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
		// run the hook after computing the response and before returning it, which is the
		// node reorganizing between FilterLogs and HeaderByNumber. It runs outside the
		// lock so it can call SetHeader and AddLog without deadlocking
		hook()
	}
	return out, nil
}

var _ ChainSource = (*FakeSource)(nil)
