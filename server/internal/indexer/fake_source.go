package indexer

import (
	"context"
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// FakeSource is an in-memory ChainSource for tests. It models a simple
// linear chain of headers plus a flat log list, and lets tests simulate a
// reorg by replacing a range of headers (giving them different hashes via
// distinct Extra bytes) and swapping which logs are "canonical" at those
// heights.
type FakeSource struct {
	mu      sync.Mutex
	headers map[uint64]*types.Header
	logs    []types.Log
	head    uint64

	// FilterLogsErrors, if non-empty, is popped (FIFO) — one entry per
	// FilterLogs call — and returned instead of a normal result when
	// non-nil, letting tests simulate a sequence of RPC failures
	// (oversized-response, timeout, ...) without real network I/O. A
	// popped nil entry means "succeed this call", so a fault can be
	// scripted at a specific position in a sequence of otherwise-successful
	// calls.
	FilterLogsErrors []error
	// MaxLogRangeBlocks, if >0, makes FilterLogs itself return an
	// "oversized" error for any query spanning more than this many blocks —
	// a standing RPC-provider-limit simulation, independent of
	// FilterLogsErrors' one-shot queue.
	MaxLogRangeBlocks uint64
	// FilterLogsCalls counts every FilterLogs invocation, so a test can
	// assert how many chunk requests a scan actually issued.
	FilterLogsCalls int
	// FilterLogsHook, if set, is invoked ONCE (then cleared), immediately
	// after a successful FilterLogs call computes its response but before
	// returning it — letting a test mutate chain state (typically
	// SetHeader, simulating a reorg) at that exact point without any real
	// concurrency — reproducing the case where the node reorganizes before
	// HeaderByNumber(N).
	FilterLogsHook func()
}

// ErrRangeTooLarge is a canned error matching isRangeTooLargeError's
// classifier, for tests exercising the shrink-on-oversized-response path.
var ErrRangeTooLarge = errors.New("query returned more than 10000 results")

// ErrTransient is a canned error that does NOT match isRangeTooLargeError,
// for tests exercising the bounded-backoff retry path.
var ErrTransient = errors.New("simulated transient RPC failure")

// NewFakeSource returns an empty FakeSource; use SetHeader/AddLog/SetHead
// to build up chain state.
func NewFakeSource() *FakeSource {
	return &FakeSource{headers: map[uint64]*types.Header{}}
}

// SetHeader sets (or replaces, simulating a reorg) the header at number,
// using seed to vary the header's hash deterministically.
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

// Logs returns a copy of every log currently tracked (i.e. after every
// AddLog/RemoveLogsAtOrAfter call so far), regardless of FilterLogs'
// from/to/address filtering — used by server/internal/shadow to compute an
// independent expected-event count for its final reconciliation step
// without duplicating this type's internal bookkeeping.
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
		// Stamp BlockHash from whichever header is CURRENTLY registered at
		// this log's block number, exactly as a real node's eth_getLogs
		// response reflects its own canonical view at query time — tests
		// need not keep AddLog's BlockHash manually in sync with
		// SetHeader, and a SetHeader call (simulating a reorg) BETWEEN two
		// FilterLogs calls naturally produces a log stamped with the OLD
		// hash on the first call and the NEW hash on a second — the exact
		// race the validation logic needs to be testable
		// against (see TestIndexerRejectsForkThatChangesBetweenFilterLogsAndHeaderByNumber
		// in indexer_test.go).
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
		// Invoked AFTER computing this response but BEFORE returning it —
		// i.e. exactly "the node reorganizes between FilterLogs and
		// HeaderByNumber". Called
		// outside the lock so the hook can itself call SetHeader/AddLog
		// without deadlocking.
		hook()
	}
	return out, nil
}

var _ ChainSource = (*FakeSource)(nil)
