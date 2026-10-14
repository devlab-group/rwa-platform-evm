// Package indexer ingests contract event logs into models.ChainEvent records
// keyed by (chainId, address, txHash, logIndex), tracks a scan checkpoint, and
// rolls back on reorg. Read models such as redemptions are rebuilt only from
// these persisted events.
package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// CheckpointAddress is the synthetic IndexerCheckpoint.Address under which the
// indexer stores its single checkpoint. It scans every configured address in one
// combined FilterLogs query, so all addresses share one scan position.
const CheckpointAddress = "ALL"

// DefaultRollbackWindow is how many blocks a detected reorg rolls back by when no
// block hash history is configured (see WithBlockHashRepository). The rollback
// re-baselines the checkpoint hash from a fresh chain read, so a reorg deeper
// than the window leaves stale events below it and is not rediscovered. Use
// WithBlockHashRepository to find the true common ancestor.
const DefaultRollbackWindow = 10

// ChainSource is the subset of blockchain.Client that the indexer needs. FakeSource
// implements it for tests, so reorg handling can be tested without a live chain.
type ChainSource interface {
	BlockNumber(ctx context.Context) (uint64, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// EventDecoder turns a raw log into a named event with decoded fields. When no
// decoder is given, the indexer names each event by its topic0 and keeps the raw
// data, which is enough to test ingestion and rollback. Deployments supply a
// decoder built from internal/bindings so read models can use Name and Data.
type EventDecoder func(log types.Log) (name string, data map[string]any, err error)

// DefaultMaxRetainedHistory is how many blocks of per-block hash history (see
// repository.BlockHashRepository) the indexer keeps. The history lets it walk
// back to a common ancestor on a reorg deeper than the rollback window.
const DefaultMaxRetainedHistory = 256

// DefaultMaxAutoReorgDepth bounds how deep a reorg the indexer resolves
// automatically. A common ancestor found within this many blocks of the
// checkpoint is rolled back to. If none can be verified within that depth, Poll
// returns ErrReconciliationRequired and stops advancing until an operator calls
// ResetToTrustedCheckpoint. The limit is independent of maxHistory: history
// retention bounds storage, while this bounds unattended rollback.
const DefaultMaxAutoReorgDepth = 100

// ErrReconciliationRequired is returned by Poll when a detected reorg's common
// ancestor cannot be verified within maxAutoReorgDepth blocks of the checkpoint.
// The indexer leaves persisted events and the checkpoint position unchanged and
// keeps returning this error on every Poll until ResetToTrustedCheckpoint is
// called with a block that the operator has verified out of band.
var ErrReconciliationRequired = errors.New("indexer: reorg exceeds automatic recovery policy; reconciliation required")

// DefaultMaxLogRange is the maximum number of blocks one FilterLogs call spans.
// Many RPC providers cap the block range of an eth_getLogs request, so the range
// is configurable. The default keeps a routine poll on a live chain to one request
// in the common case. 0 means unbounded: a single request for the whole range, for
// a provider known to have no limit.
const DefaultMaxLogRange = 2000

// DefaultMaxRangeRetries is how many attempts scanChunk makes against a transient
// FilterLogs failure, with exponential backoff, before returning the error from
// Poll. Range-too-large and timeout errors shrink the range instead and do not use
// up attempts (see scanChunk).
const DefaultMaxRangeRetries = 5

// initialRangeBackoff and maxRangeBackoff bound the exponential backoff between
// retries of a failing chunk
const (
	initialRangeBackoff = 200 * time.Millisecond
	maxRangeBackoff     = 5 * time.Second
)

// Indexer scans a fixed set of contract addresses on one chain.
type Indexer struct {
	source            ChainSource
	checkpoints       repository.IndexerCheckpointRepository
	events            repository.ChainEventRepository
	blockHashes       repository.BlockHashRepository
	chunks            repository.ChainChunkRepository
	dlq               repository.DeadLetterRepository
	chainID           int64
	addresses         []common.Address
	decode            EventDecoder
	startBlock        uint64
	rollbackWindow    uint64
	maxHistory        uint64
	maxAutoReorgDepth uint64
	maxLogRange       uint64
	maxRangeRetries   int
	// sleep waits out the retry backoff; tests replace it (see withSleepFunc) so
	// retries run instantly
	sleep func(time.Duration)
}

// Option configures optional Indexer behavior.
type Option func(*Indexer)

// WithRollbackWindow overrides DefaultRollbackWindow.
func WithRollbackWindow(blocks uint64) Option {
	return func(idx *Indexer) { idx.rollbackWindow = blocks }
}

// WithBlockHashRepository enables deep-reorg protection. Every committed chunk
// records block hashes, and a detected reorg walks back through that history to
// find the common ancestor. Without this option, rollback uses a fixed window
// (see DefaultRollbackWindow), which cannot tell whether the fork is deeper than
// the window.
func WithBlockHashRepository(repo repository.BlockHashRepository) Option {
	return func(idx *Indexer) { idx.blockHashes = repo }
}

// WithMaxRetainedHistory overrides DefaultMaxRetainedHistory.
func WithMaxRetainedHistory(blocks uint64) Option {
	return func(idx *Indexer) { idx.maxHistory = blocks }
}

// WithChunkCommitter writes chunk events, block hashes, pruning and checkpoint
// advance through repo.CommitChunk in one transaction. A checkpoint version
// check prevents replicas from overwriting a newer checkpoint. Rollbacks and
// ResetToTrustedCheckpoint use repo.RewindChunk with the same version check.
// Without this option, separate writes require a single writer.
//
// Setting ReconciliationRequired after an unresolved deep reorg bypasses the
// version check: pausing must succeed even if a concurrent checkpoint changes.
func WithChunkCommitter(repo repository.ChainChunkRepository) Option {
	return func(idx *Indexer) { idx.chunks = repo }
}

// WithMaxAutoReorgDepth overrides DefaultMaxAutoReorgDepth.
func WithMaxAutoReorgDepth(blocks uint64) Option {
	return func(idx *Indexer) { idx.maxAutoReorgDepth = blocks }
}

// WithMaxLogRange overrides DefaultMaxLogRange. 0 means unbounded.
func WithMaxLogRange(blocks uint64) Option {
	return func(idx *Indexer) { idx.maxLogRange = blocks }
}

// WithMaxRangeRetries overrides DefaultMaxRangeRetries.
func WithMaxRangeRetries(attempts int) Option {
	return func(idx *Indexer) { idx.maxRangeRetries = attempts }
}

// WithDeadLetterQueue enables the dead-letter policy: a log that cannot be decoded
// is recorded to repo and Poll continues past it (see handleDecodeFailure).
// Without this option a decode failure aborts Poll, and every later log in the
// same FilterLogs response waits behind it.
func WithDeadLetterQueue(repo repository.DeadLetterRepository) Option {
	return func(idx *Indexer) { idx.dlq = repo }
}

// withSleepFunc overrides the backoff wait function, for tests
func withSleepFunc(sleep func(time.Duration)) Option {
	return func(idx *Indexer) { idx.sleep = sleep }
}

// New constructs an Indexer for chainID scanning addresses, starting from
// startBlock when no checkpoint exists. startBlock is also the trusted start
// (deployment) block: no event this indexer persisted predates it, so the
// common-ancestor walk-back never needs to go below it and rebuilding from it is
// a safe baseline regardless of maxAutoReorgDepth. A nil decoder falls back to a
// topic0-only decode.
func New(source ChainSource, checkpoints repository.IndexerCheckpointRepository, events repository.ChainEventRepository, chainID int64, addresses []common.Address, startBlock uint64, decode EventDecoder, opts ...Option) *Indexer {
	if decode == nil {
		decode = defaultDecode
	}
	idx := &Indexer{
		source: source, checkpoints: checkpoints, events: events,
		chainID: chainID, addresses: addresses, decode: decode, startBlock: startBlock,
		rollbackWindow: DefaultRollbackWindow, maxHistory: DefaultMaxRetainedHistory,
		maxAutoReorgDepth: DefaultMaxAutoReorgDepth,
		maxLogRange:       DefaultMaxLogRange,
		maxRangeRetries:   DefaultMaxRangeRetries,
		sleep:             time.Sleep,
	}
	for _, opt := range opts {
		opt(idx)
	}
	return idx
}

func defaultDecode(log types.Log) (string, map[string]any, error) {
	name := "unknown"
	if len(log.Topics) > 0 {
		name = log.Topics[0].Hex()
	}
	return name, map[string]any{"data": "0x" + common.Bytes2Hex(log.Data)}, nil
}

// Poll performs one scan cycle: it rolls back a reorg if the checkpointed block's
// hash no longer matches the chain, then ingests new logs up to the current head
// one bounded chunk at a time. It is safe to call repeatedly, for example on a
// timer, because ingestion skips events that already exist (ChainEventRepository.
// Exists).
//
// If an earlier Poll set ReconciliationRequired on the checkpoint (a reorg deeper
// than the automatic recovery policy), Poll returns ErrReconciliationRequired
// without reading chain state, so read models do not advance past an unresolved
// divergence. ResetToTrustedCheckpoint clears the state.
func (idx *Indexer) Poll(ctx context.Context) error {
	cp, existed, err := idx.loadCheckpoint(ctx)
	if err != nil {
		return err
	}
	if existed && cp.ReconciliationRequired {
		return fmt.Errorf("%w: chain %d paused at block %d, call ResetToTrustedCheckpoint after out-of-band verification", ErrReconciliationRequired, idx.chainID, cp.LastBlock)
	}

	// Read the current head before comparing it with the checkpoint. Reading the
	// checkpoint's header first would make an RPC whose head fell below the
	// checkpoint (a failover to a lagging node, or a reorg that shortened the chain)
	// fail HeaderByNumber(cp.LastBlock) on every poll instead of being handled as a
	// reorg
	currentBlock, err := idx.source.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("indexer: BlockNumber: %w", err)
	}

	if existed && cp.LastBlock > 0 && cp.LastBlockHash != "" {
		if currentBlock < cp.LastBlock {
			// head regression: cp.LastBlock does not exist on this source, so treat it as a
			// reorg and walk back from the lower head
			cp, err = idx.rollback(ctx, cp, currentBlock)
			if err != nil {
				// same clean stop as a lost version race on a forward commit: another replica
				// moved the checkpoint, so this rollback was computed against stale state
				if errors.Is(err, errCheckpointVersionConflict) {
					return nil
				}
				return err
			}
			existed = true
		} else {
			header, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(cp.LastBlock))
			if err != nil {
				return fmt.Errorf("indexer: fetch checkpoint header: %w", err)
			}
			if header.Hash().Hex() != cp.LastBlockHash {
				cp, err = idx.rollback(ctx, cp, cp.LastBlock)
				if err != nil {
					if errors.Is(err, errCheckpointVersionConflict) {
						return nil // see the head-regression rollback above
					}
					return err
				}
				existed = true
			}
		}
	}

	// a fresh indexer persists a baseline checkpoint even when there is nothing to
	// scan, so later polls have a stored hash to compare against
	if existed && currentBlock <= cp.LastBlock {
		return nil
	}

	fromBlock := cp.LastBlock + 1
	if !existed {
		fromBlock = idx.startBlock
	}
	if fromBlock > currentBlock {
		fromBlock = currentBlock
	}

	// Scan and commit one bounded, hash-validated chunk at a time. Accumulating the
	// whole [fromBlock,currentBlock] range and writing a checkpoint at the end would
	// let a reorg that races the scan commit fork-A events under a fork-B checkpoint
	// hash, because the checkpoint mismatch check at the start of Poll only catches
	// a reorg that finished before the poll began. scanAndCommitChunk instead reads
	// the chunk's boundary header before and after scanning and compares every log's
	// BlockHash with the canonical header at its height, discarding and retrying the
	// whole chunk if anything disagrees. Chunking also frees memory between chunks
	// and lets a restart resume after the last committed chunk during a large
	// backfill.
	for fromBlock <= currentBlock {
		chunkTo := currentBlock
		if idx.maxLogRange > 0 && fromBlock+idx.maxLogRange-1 < currentBlock {
			chunkTo = fromBlock + idx.maxLogRange - 1
		}
		nextFrom, err := idx.scanAndCommitChunk(ctx, fromBlock, chunkTo, currentBlock, cp)
		if err != nil {
			// another indexer replica advanced the checkpoint under this atomic commit:
			// stop this poll cleanly, and the next poll reads the new checkpoint and continues
			if errors.Is(err, errCheckpointVersionConflict) {
				return nil
			}
			return err
		}
		fromBlock = nextFrom
	}
	return nil
}

// errCheckpointVersionConflict is returned internally when a commit or rewind
// lost the checkpoint-version check to a concurrent indexer replica. Poll treats
// it as a clean stop and never returns it to the caller.
var errCheckpointVersionConflict = errors.New("indexer: checkpoint advanced by a concurrent writer")

// scanAndCommitChunk validates [from,wantTo] as a coherent chain snapshot and, only
// if validation succeeds, commits it: it ingests every log, records block hashes
// within the retention window and advances the checkpoint to the chunk's actual
// upper bound, all through commitChunk. A failed validation therefore retries one
// bounded chunk. currentBlock is the chain head this Poll observed, which can be
// above the chunk's upper bound. It anchors the block hash retention window to
// the true head instead of the chunk end during a multi-chunk backfill.
//
// It returns the next fromBlock to scan, the committed upper bound plus one.
func (idx *Indexer) scanAndCommitChunk(ctx context.Context, from, wantTo, currentBlock uint64, cp *models.IndexerCheckpoint) (uint64, error) {
	backoff := initialRangeBackoff
	for attempt := 1; ; attempt++ {
		// capture the boundary hash before scanning
		beforeHeader, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(wantTo))
		if err != nil {
			return 0, fmt.Errorf("indexer: fetch chunk boundary header at %d: %w", wantTo, err)
		}

		logs, actualTo, err := idx.scanChunk(ctx, from, wantTo)
		if err != nil {
			return 0, err
		}

		// re-read the boundary after scanning and retry the range if it changed. A reorg
		// during the FilterLogs call itself changes the boundary hash. The re-read covers
		// the upper bound scanChunk actually reached, which can be below wantTo
		afterHeader, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(actualTo))
		if err != nil {
			return 0, fmt.Errorf("indexer: fetch chunk boundary header at %d: %w", actualTo, err)
		}
		valid := actualTo == wantTo && afterHeader.Hash() == beforeHeader.Hash()
		// when actualTo differs from wantTo, scanChunk shrank the range because of an
		// oversized-response or timeout error, not a reorg. The boundary comparison is
		// meaningless for a boundary that was not held constant, so accept it and rely on
		// the per-log validation below
		if actualTo != wantTo {
			valid = true
		}

		var validated []types.Log
		var headerCache map[uint64]common.Hash
		if valid {
			// validate every returned log's BlockHash against the canonical header at its
			// block number before committing, which catches a reorg inside the range even if
			// the boundary hash converges by actualTo
			validated, headerCache, valid, err = idx.validateLogsAgainstCanonical(ctx, logs, actualTo, afterHeader.Hash())
			if err != nil {
				return 0, err
			}
		}

		if valid {
			if err := idx.commitChunk(ctx, from, actualTo, currentBlock, afterHeader.Hash().Hex(), validated, headerCache, cp); err != nil {
				return 0, err
			}
			return actualTo + 1, nil
		}

		// the chain moved mid-scan, or a log disagreed with the canonical header just
		// re-read: discard this attempt (nothing was ingested) and retry the same range
		// fresh, bounded like any other transient failure
		if attempt >= idx.maxRangeRetries {
			return 0, fmt.Errorf("indexer: chunk [%d,%d] could not be validated as a coherent snapshot after %d attempt(s); possible sustained reorg racing the scan", from, wantTo, attempt)
		}
		idx.sleep(backoff)
		backoff *= 2
		if backoff > maxRangeBackoff {
			backoff = maxRangeBackoff
		}
	}
}

// validateLogsAgainstCanonical confirms that every log's BlockHash matches the
// canonical header at its own block number and returns the logs that pass, with
// ok=true. Logs the RPC node flagged Removed are dropped, since the node is
// reporting that their block is no longer canonical. toHash, the hash of the
// chunk's upper bound, seeds the returned header cache so commitChunk can reuse
// it.
//
// If any log disagrees, ok=false and the caller discards the whole attempt (see
// scanAndCommitChunk) instead of committing the logs that matched. A mismatch
// means a reorg may still be in progress across the range, and committing part of
// it would mix forks.
func (idx *Indexer) validateLogsAgainstCanonical(ctx context.Context, logs []types.Log, to uint64, toHash common.Hash) (validated []types.Log, headerCache map[uint64]common.Hash, ok bool, err error) {
	headerCache = map[uint64]common.Hash{to: toHash}
	validated = make([]types.Log, 0, len(logs))
	for _, log := range logs {
		if log.Removed {
			continue
		}
		canonical, known := headerCache[log.BlockNumber]
		if !known {
			h, herr := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(log.BlockNumber))
			if herr != nil {
				return nil, nil, false, fmt.Errorf("indexer: validate log %s#%d against block %d: %w", log.TxHash.Hex(), log.Index, log.BlockNumber, herr)
			}
			canonical = h.Hash()
			headerCache[log.BlockNumber] = canonical
		}
		if log.BlockHash != canonical {
			return nil, nil, false, nil
		}
		validated = append(validated, log)
	}
	return validated, headerCache, true, nil
}

// commitChunk persists everything scanAndCommitChunk validated for [from,to]: the
// decoded events, block hashes within the retention window (reusing headerCache
// where possible), history pruning, and the checkpoint advance to (to, toHash).
//
// With a ChainChunkRepository (WithChunkCommitter), these land in one atomic
// CommitChunk call, and the checkpoint advance is conditional on cp.Version so a
// concurrent replica cannot overwrite a newer checkpoint with an older one. cp is
// advanced in place on success. Without a committer the writes are separate,
// which is correct only for a single writer. currentBlock is the head of the
// overall poll, not this chunk's `to`, and anchors the retention window.
func (idx *Indexer) commitChunk(ctx context.Context, from, to, currentBlock uint64, toHash string, logs []types.Log, headerCache map[uint64]common.Hash, cp *models.IndexerCheckpoint) error {
	// decode, and record decode failures in the DLQ, before the atomic commit: a DLQ
	// write is separate from the transactional event set. Events that already exist
	// are skipped, so the commit carries only new rows
	events, err := idx.prepareChunkEvents(ctx, logs)
	if err != nil {
		return err
	}

	var blockHashes []repository.BlockHashEntry
	var pruneBefore *uint64
	if idx.blockHashes != nil || idx.chunks != nil {
		recordFrom := from
		if currentBlock > idx.maxHistory {
			if floor := currentBlock - idx.maxHistory; recordFrom < floor {
				recordFrom = floor
			}
		}
		for bn := recordFrom; bn <= to; bn++ {
			hash, ok := headerCache[bn]
			if !ok {
				h, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(bn))
				if err != nil {
					return fmt.Errorf("indexer: fetch block %d header for hash history: %w", bn, err)
				}
				hash = h.Hash()
				headerCache[bn] = hash
			}
			blockHashes = append(blockHashes, repository.BlockHashEntry{BlockNumber: bn, Hash: hash.Hex()})
		}
		if currentBlock > idx.maxHistory {
			pb := currentBlock - idx.maxHistory
			pruneBefore = &pb
		}
	}

	newCp := &models.IndexerCheckpoint{
		ChainID: idx.chainID, Address: CheckpointAddress,
		LastBlock: to, LastBlockHash: toHash, UpdatedAt: time.Now().UTC(),
	}

	if idx.chunks != nil {
		committed, err := idx.chunks.CommitChunk(ctx, repository.ChunkCommit{
			ChainID: idx.chainID, Events: events, BlockHashes: blockHashes, PruneBlockHashesBefore: pruneBefore,
			Checkpoint: newCp, ExpectedCheckpointVersion: cp.Version,
		})
		if err != nil {
			return err
		}
		if !committed {
			return errCheckpointVersionConflict
		}
		cp.LastBlock = to
		cp.LastBlockHash = toHash
		cp.Version++
		cp.UpdatedAt = newCp.UpdatedAt
		return nil
	}

	// fallback without an atomic committer: separate writes
	for _, e := range events {
		if err := idx.events.Create(ctx, e); err != nil {
			return fmt.Errorf("indexer: persist event: %w", err)
		}
	}
	if idx.blockHashes != nil {
		for _, bh := range blockHashes {
			if err := idx.blockHashes.Record(ctx, idx.chainID, bh.BlockNumber, bh.Hash); err != nil {
				return fmt.Errorf("indexer: record block hash: %w", err)
			}
		}
		if pruneBefore != nil {
			if err := idx.blockHashes.PruneBefore(ctx, idx.chainID, *pruneBefore); err != nil {
				return fmt.Errorf("indexer: prune block hash history: %w", err)
			}
		}
	}
	if err := idx.checkpoints.Set(ctx, newCp); err != nil {
		return err
	}
	cp.LastBlock = to
	cp.LastBlockHash = toHash
	cp.UpdatedAt = newCp.UpdatedAt
	return nil
}

// prepareChunkEvents decodes every log that is not yet ingested into a
// models.ChainEvent. A decode failure goes to the DLQ, outside any atomic chunk
// commit. Logs that already exist are skipped, so the result is exactly the new
// rows to persist.
func (idx *Indexer) prepareChunkEvents(ctx context.Context, logs []types.Log) ([]*models.ChainEvent, error) {
	var events []*models.ChainEvent
	for _, log := range logs {
		key := models.EventKey{ChainID: idx.chainID, Address: log.Address.Hex(), TxHash: log.TxHash.Hex(), LogIndex: log.Index}
		exists, err := idx.events.Exists(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("indexer: check existing event: %w", err)
		}
		if exists {
			continue
		}
		name, data, err := idx.decode(log)
		if err != nil {
			if derr := idx.handleDecodeFailure(ctx, log, err); derr != nil {
				return nil, derr
			}
			continue
		}
		events = append(events, buildEvent(idx.chainID, log, name, data))
	}
	return events, nil
}

// scanChunk fetches [from,to] with retries and returns the logs and the highest
// block the successful request covered, which is `to` unless a range-too-large or
// timeout error shrank the range first
func (idx *Indexer) scanChunk(ctx context.Context, from, to uint64) ([]types.Log, uint64, error) {
	backoff := initialRangeBackoff
	for attempt := 1; ; attempt++ {
		logs, err := idx.source.FilterLogs(ctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(from),
			ToBlock:   new(big.Int).SetUint64(to),
			Addresses: idx.addresses,
		})
		if err == nil {
			return logs, to, nil
		}
		// shrink the range on timeouts as well as explicit provider size errors, since a
		// provider timeout often depends on response size and retrying the same span
		// would only exhaust the retry budget
		if (isRangeTooLargeError(err) || isTimeoutError(err)) && to > from {
			// not a transient failure: halve the span and retry the same fromBlock at once,
			// without using up a backoff attempt
			newSpan := (to - from + 1) / 2
			if newSpan < 1 {
				newSpan = 1
			}
			to = from + newSpan - 1
			continue
		}
		if attempt >= idx.maxRangeRetries {
			return nil, 0, fmt.Errorf("indexer: FilterLogs [%d,%d] failed after %d attempt(s): %w", from, to, attempt, err)
		}
		idx.sleep(backoff)
		backoff *= 2
		if backoff > maxRangeBackoff {
			backoff = maxRangeBackoff
		}
	}
}

// isRangeTooLargeError reports whether a FilterLogs error is the RPC provider
// rejecting the requested block range or response size, so the caller can shrink
// the range. JSON-RPC defines no standard error code for this and providers word
// it differently, so it matches common message fragments.
func isRangeTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"query returned more than",
		"block range",
		"range too large",
		"range is too large",
		"too many results",
		"response size exceeded",
		"limit exceeded",
		"exceeds the range",
		"more than 10000 results",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// isTimeoutError reports whether a FilterLogs error is a deadline or timeout, so
// the caller can shrink the range as it does for a size error. It checks
// context.DeadlineExceeded, net.Error timeouts and common message wording,
// including "context canceled".
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"timeout", "timed out", "deadline exceeded", "context canceled", "i/o timeout"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// loadCheckpoint returns the persisted checkpoint and whether one existed. A
// missing checkpoint (the first poll) is distinct from one stored at the zero
// value, so Poll can tell "nothing persisted yet" from "persisted at genesis".
func (idx *Indexer) loadCheckpoint(ctx context.Context) (*models.IndexerCheckpoint, bool, error) {
	cp, err := idx.checkpoints.Get(ctx, idx.chainID, CheckpointAddress)
	if err == nil {
		return cp, true, nil
	}
	if err != repository.ErrNotFound {
		return nil, false, fmt.Errorf("indexer: load checkpoint: %w", err)
	}
	return &models.IndexerCheckpoint{ChainID: idx.chainID, Address: CheckpointAddress, LastBlock: idx.startBlock}, false, nil
}

// buildEvent constructs the ChainEvent for a decoded log, for prepareChunkEvents
// and RetryDeadLetter
func buildEvent(chainID int64, log types.Log, name string, data map[string]any) *models.ChainEvent {
	return &models.ChainEvent{
		ChainID: chainID, Address: log.Address.Hex(), TxHash: log.TxHash.Hex(), LogIndex: log.Index,
		BlockNumber: log.BlockNumber, BlockHash: log.BlockHash.Hex(), Name: name, Data: data,
		IndexedAt: time.Now().UTC(),
	}
}

// handleDecodeFailure applies the DLQ policy: an undecodable log is persisted to
// the DLQ instead of being dropped, and scanning continues. Without a DLQ it
// returns an error that aborts Poll (see WithDeadLetterQueue).
func (idx *Indexer) handleDecodeFailure(ctx context.Context, log types.Log, cause error) error {
	if idx.dlq == nil {
		return fmt.Errorf("indexer: decode log %s#%d: %w", log.TxHash.Hex(), log.Index, cause)
	}
	if err := idx.recordDeadLetter(ctx, log, cause); err != nil {
		return err
	}
	// the failure is recorded for triage through RetryDeadLetter, so Poll continues
	// past this log and the checkpoint still advances. The event is deferred and
	// visible, not lost
	return nil
}

// deadLetterID identifies one failed log deterministically, so repeated failures
// across polls update one DeadLetterRepository entry (bumping RetryCount) instead
// of growing the queue
func deadLetterID(chainID int64, log types.Log) string {
	return fmt.Sprintf("%d:%s:%d", chainID, log.TxHash.Hex(), log.Index)
}

// recordDeadLetter persists log's failure to the DLQ together with the full log,
// so an operator retry does not need to query a chain that may have reorged since
func (idx *Indexer) recordDeadLetter(ctx context.Context, log types.Log, cause error) error {
	sig := ""
	if len(log.Topics) > 0 {
		sig = log.Topics[0].Hex()
	}
	raw, merr := json.Marshal(log)
	if merr != nil {
		raw = nil // still record the failure even if the log does not marshal
	}
	now := time.Now().UTC()
	entry := &models.DeadLetterEntry{
		ID: deadLetterID(idx.chainID, log), ChainID: idx.chainID,
		BlockNumber: log.BlockNumber, BlockHash: log.BlockHash.Hex(),
		TxHash: log.TxHash.Hex(), LogIndex: log.Index, EventSignature: sig,
		ErrorCategory: models.DLQErrorDecode, ErrorMessage: cause.Error(),
		FirstFailedAt: now, LastFailedAt: now, SourceData: raw,
	}
	if err := idx.dlq.Record(ctx, entry); err != nil {
		return fmt.Errorf("indexer: record DLQ entry for log %s#%d: %w", log.TxHash.Hex(), log.Index, err)
	}
	return nil
}

// RetryDeadLetter decodes and ingests the log retained in a DLQ entry again. On
// success the entry is marked Resolved. On a repeated decode failure the entry is
// re-recorded (bumping RetryCount) and the error is returned. If the log's block
// is no longer canonical, the entry is dismissed as orphaned without ingesting.
func (idx *Indexer) RetryDeadLetter(ctx context.Context, id string) error {
	if idx.dlq == nil {
		return errors.New("indexer: no dead letter queue configured")
	}
	entry, err := idx.dlq.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("indexer: load DLQ entry %s: %w", id, err)
	}
	var log types.Log
	if err := json.Unmarshal(entry.SourceData, &log); err != nil {
		return fmt.Errorf("indexer: DLQ entry %s: decode retained source data: %w", id, err)
	}

	// a DLQ entry can stay unresolved for a long time, and the block it came from may
	// no longer be canonical. Require the canonical header's hash to equal the
	// retained log's BlockHash and dismiss an orphan instead of ingesting it, which
	// would bring back an event from an abandoned fork
	header, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(log.BlockNumber))
	if err != nil {
		return fmt.Errorf("indexer: DLQ entry %s: fetch canonical header at %d: %w", id, log.BlockNumber, err)
	}
	if header.Hash() != log.BlockHash {
		if err := idx.dlq.Record(ctx, &models.DeadLetterEntry{
			ID: entry.ID, ChainID: entry.ChainID, BlockNumber: entry.BlockNumber, BlockHash: entry.BlockHash,
			TxHash: entry.TxHash, LogIndex: entry.LogIndex, EventSignature: entry.EventSignature,
			ErrorCategory: models.DLQErrorOrphaned,
			ErrorMessage:  fmt.Sprintf("block %d is no longer canonical (retained hash %s, current %s); dismissed without ingesting", log.BlockNumber, log.BlockHash.Hex(), header.Hash().Hex()),
			SourceData:    entry.SourceData,
		}); err != nil {
			return fmt.Errorf("indexer: DLQ entry %s: record orphan dismissal: %w", id, err)
		}
		return idx.dlq.Resolve(ctx, id) // dismissed, not ingested: the log is orphaned, not a failure to fix
	}

	name, data, err := idx.decode(log)
	if err != nil {
		return idx.recordDeadLetter(ctx, log, err)
	}
	key := models.EventKey{ChainID: idx.chainID, Address: log.Address.Hex(), TxHash: log.TxHash.Hex(), LogIndex: log.Index}
	exists, err := idx.events.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("indexer: check existing event: %w", err)
	}
	if !exists {
		if err := idx.events.Create(ctx, buildEvent(idx.chainID, log, name, data)); err != nil {
			return fmt.Errorf("indexer: persist retried event: %w", err)
		}
	}
	return idx.dlq.Resolve(ctx, id)
}

// rollback deletes every event from the block after the common ancestor onward,
// for every monitored address, and moves the checkpoint back to that ancestor,
// re-baselining LastBlockHash from a fresh chain read. searchFrom is the highest
// block the ancestor search considers: cp.LastBlock after a hash mismatch, or the
// lower current head after a head regression, when cp.LastBlock may not exist on
// the source.
//
// If findCommonAncestor returns ErrReconciliationRequired, rollback deletes no
// events and does not move the checkpoint. It only sets ReconciliationRequired on
// the stored checkpoint, so every later Poll stops at the same unresolved state
// instead of trusting an unverified point.
func (idx *Indexer) rollback(ctx context.Context, cp *models.IndexerCheckpoint, searchFrom uint64) (*models.IndexerCheckpoint, error) {
	ancestor, ancestorHash, err := idx.findCommonAncestor(ctx, cp, searchFrom)
	if err != nil {
		if errors.Is(err, ErrReconciliationRequired) {
			flagged := *cp
			flagged.ReconciliationRequired = true
			flagged.UpdatedAt = time.Now().UTC()
			if setErr := idx.checkpoints.Set(ctx, &flagged); setErr != nil {
				return nil, fmt.Errorf("indexer: persist reconciliation-required checkpoint: %w", setErr)
			}
		}
		return nil, err
	}
	newCp := &models.IndexerCheckpoint{
		ChainID: idx.chainID, Address: CheckpointAddress,
		LastBlock: ancestor, LastBlockHash: ancestorHash, UpdatedAt: time.Now().UTC(),
	}
	return idx.rewind(ctx, cp, newCp, ancestor+1, "rollback")
}

// rewind deletes events at or above deleteFrom and stores newCp for a reorg
// rollback or trusted reset. With a chunk committer, deletion and checkpoint
// update are one version-checked transaction, preventing a concurrent forward
// commit from leaving fork events above the rewound checkpoint.
//
// A conflict returns errCheckpointVersionConflict. Poll stops cleanly, then
// rechecks the advanced checkpoint on its next run. Without a chunk committer,
// the separate writes require a single writer.
func (idx *Indexer) rewind(ctx context.Context, cp, newCp *models.IndexerCheckpoint, deleteFrom uint64, what string) (*models.IndexerCheckpoint, error) {
	if idx.chunks != nil {
		expected := 0
		if cp != nil {
			expected = cp.Version
		}
		addrs := make([]string, 0, len(idx.addresses))
		for _, addr := range idx.addresses {
			addrs = append(addrs, addr.Hex())
		}
		committed, err := idx.chunks.RewindChunk(ctx, repository.ChunkRewind{
			ChainID: idx.chainID, Addresses: addrs, DeleteFromBlock: deleteFrom,
			Checkpoint: newCp, ExpectedCheckpointVersion: expected,
		})
		if err != nil {
			return nil, fmt.Errorf("indexer: %s rewind: %w", what, err)
		}
		if !committed {
			return nil, errCheckpointVersionConflict
		}
		newCp.Version = expected + 1
		return newCp, nil
	}

	// fallback without an atomic committer: separate writes
	for _, addr := range idx.addresses {
		if _, err := idx.events.DeleteFromBlock(ctx, idx.chainID, addr.Hex(), deleteFrom); err != nil {
			return nil, fmt.Errorf("indexer: %s DeleteFromBlock: %w", what, err)
		}
	}
	if err := idx.checkpoints.Set(ctx, newCp); err != nil {
		return nil, fmt.Errorf("indexer: persist %s checkpoint: %w", what, err)
	}
	return newCp, nil
}

// findCommonAncestor walks retained hashes at or below searchFrom to find the
// highest block matching a fresh chain read. It stops at the larger of startBlock
// and cp.LastBlock - maxAutoReorgDepth.
//
// Reaching the trusted startBlock permits rebuilding: no persisted event predates
// it. Reaching the depth limit first returns ErrReconciliationRequired because
// the fork may extend beyond the verified range. Without WithBlockHashRepository,
// use the fixed-window rollback.
func (idx *Indexer) findCommonAncestor(ctx context.Context, cp *models.IndexerCheckpoint, searchFrom uint64) (uint64, string, error) {
	if idx.blockHashes == nil {
		return idx.rollbackToFixedWindow(ctx, searchFrom)
	}

	depthFloor := uint64(0)
	if cp.LastBlock > idx.maxAutoReorgDepth {
		depthFloor = cp.LastBlock - idx.maxAutoReorgDepth
	}
	trustedFloor := idx.startBlock
	policyFloor := depthFloor
	if trustedFloor > policyFloor {
		policyFloor = trustedFloor
	}
	// autoRecoverable depends on which floor is higher, not on where the search
	// started: reaching the trusted start block is safe because it is the deployment
	// boundary, while reaching the depth limit before it is not
	autoRecoverable := trustedFloor >= depthFloor

	for candidate := searchFrom; candidate > policyFloor; candidate-- {
		storedHash, err := idx.blockHashes.Get(ctx, idx.chainID, candidate)
		switch {
		case err == nil:
			header, herr := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(candidate))
			if herr != nil {
				return 0, "", fmt.Errorf("indexer: fetch ancestor candidate header: %w", herr)
			}
			if header.Hash().Hex() == storedHash {
				return candidate, storedHash, nil // found a real common ancestor
			}
		case !errors.Is(err, repository.ErrNotFound):
			return 0, "", fmt.Errorf("indexer: load retained hash at %d: %w", candidate, err)
		}
		// err was ErrNotFound (no retained hash at this height) or the stored hash did not
		// match (still inside the fork): keep walking
	}

	if !autoRecoverable {
		return 0, "", fmt.Errorf("indexer: %w: no verified common ancestor within %d blocks of checkpoint block %d (chain %d)",
			ErrReconciliationRequired, idx.maxAutoReorgDepth, cp.LastBlock, idx.chainID)
	}

	rebuildAt := policyFloor
	if rebuildAt > searchFrom {
		rebuildAt = searchFrom // head regressed below even the trusted-start floor
	}
	if rebuildAt == 0 {
		return 0, "", nil // genesis: nothing to compare, always the trusted baseline
	}
	header, herr := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(rebuildAt))
	if herr != nil {
		return 0, "", fmt.Errorf("indexer: fetch trusted rebuild checkpoint header: %w", herr)
	}
	return rebuildAt, header.Hash().Hex(), nil
}

// rollbackToFixedWindow is the fallback for an Indexer built without
// WithBlockHashRepository: it rolls back a fixed window from searchFrom (not
// necessarily cp.LastBlock, see rollback), never below startBlock, and
// re-baselines unconditionally. See DefaultRollbackWindow for its limitation.
func (idx *Indexer) rollbackToFixedWindow(ctx context.Context, searchFrom uint64) (uint64, string, error) {
	var rollbackTo uint64
	if searchFrom > idx.rollbackWindow {
		rollbackTo = searchFrom - idx.rollbackWindow
	}
	if rollbackTo < idx.startBlock {
		rollbackTo = idx.startBlock
	}
	if rollbackTo == 0 {
		return 0, "", nil
	}
	header, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(rollbackTo))
	if err != nil {
		return 0, "", fmt.Errorf("indexer: rollback fetch ancestor header: %w", err)
	}
	return rollbackTo, header.Hash().Hex(), nil
}

// ResetToTrustedCheckpoint recovers from ErrReconciliationRequired by making
// trustedBlock, which the operator has verified out of band (for example against a
// block explorer or another trusted node), the new baseline. It deletes every
// monitored address's events after trustedBlock as an automatic rollback would,
// re-baselines the checkpoint at trustedBlock with a hash read fresh from the
// chain, and clears ReconciliationRequired so Poll resumes. It is safe to call
// when the indexer is not flagged, for example to force a resync.
func (idx *Indexer) ResetToTrustedCheckpoint(ctx context.Context, trustedBlock uint64) error {
	var hash string
	if trustedBlock > 0 {
		header, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(trustedBlock))
		if err != nil {
			return fmt.Errorf("indexer: fetch trusted checkpoint header: %w", err)
		}
		hash = header.Hash().Hex()
	}
	// the rewind checks the checkpoint version this reset was computed against, so a
	// running replica that advances the checkpoint mid-reset makes the reset fail
	// instead of leaving that replica's events above the rewound checkpoint. An absent
	// checkpoint reads as version 0, the baseline of a fresh indexer's first commit
	cur, err := idx.checkpoints.Get(ctx, idx.chainID, CheckpointAddress)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("indexer: read checkpoint before reset: %w", err)
	}
	newCp := &models.IndexerCheckpoint{
		ChainID: idx.chainID, Address: CheckpointAddress,
		LastBlock: trustedBlock, LastBlockHash: hash, UpdatedAt: time.Now().UTC(),
	}
	if _, err := idx.rewind(ctx, cur, newCp, trustedBlock+1, "reset"); err != nil {
		if errors.Is(err, errCheckpointVersionConflict) {
			return fmt.Errorf("indexer: reset to trusted checkpoint %d: the stored checkpoint moved while the reset was being applied (another indexer replica is running); stop it and retry", trustedBlock)
		}
		return err
	}
	return nil
}
