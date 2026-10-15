// Package indexer stores contract logs as ChainEvents keyed by (chainId, address,
// txHash, logIndex), checkpoints progress and rolls back reorgs. Read models rebuild
// from these persisted events.
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

// CheckpointAddress is the shared synthetic checkpoint for all addresses scanned by one
// combined FilterLogs query
const CheckpointAddress = "ALL"

// DefaultRollbackWindow limits fallback rollback without block-hash history.
// Re-baselining hides forks deeper than that window, leaving stale events below it.
// WithBlockHashRepository enables common-ancestor recovery.
const DefaultRollbackWindow = 10

// ChainSource exposes the indexer's RPC subset; FakeSource supports reorg tests without
// a live chain
type ChainSource interface {
	BlockNumber(ctx context.Context) (uint64, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// EventDecoder produces event Name/Data from logs. A nil decoder retains raw data named
// by topic0 for ingestion tests; deployments use bindings-based decoding for read
// models.
type EventDecoder func(log types.Log) (name string, data map[string]any, err error)

// DefaultMaxRetainedHistory bounds stored per-block hashes for ancestor searches beyond
// the fallback rollback window
const DefaultMaxRetainedHistory = 256

// DefaultMaxAutoReorgDepth limits ancestor search independently of history retention.
// With block-hash history configured, unresolved forks pause Poll until
// ResetToTrustedCheckpoint; reaching a trusted start within policy permits rebuilding.
const DefaultMaxAutoReorgDepth = 100

// ErrReconciliationRequired pauses Poll when history-based recovery cannot verify an
// ancestor within policy. Indexed events remain; subsequent polls stop until an
// operator verifies and sets a trusted checkpoint.
var ErrReconciliationRequired = errors.New("indexer: reorg exceeds automatic recovery policy; reconciliation required")

// DefaultMaxLogRange caps blocks per FilterLogs request for provider range limits.
// WithMaxLogRange overrides it; 0 requests the full remaining range.
const DefaultMaxLogRange = 2000

// DefaultMaxRangeRetries bounds scanChunk's retry loop. Ordinary failures back off;
// range-too-large/timeouts shrink multi-block spans immediately, skipping backoff and
// the immediate exhaustion check while still incrementing the loop's attempt counter.
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

// WithBlockHashRepository records retained hashes for ancestor searches during reorg
// recovery. Without it, fixed-window rollback cannot detect deeper divergence; see
// DefaultRollbackWindow.
func WithBlockHashRepository(repo repository.BlockHashRepository) Option {
	return func(idx *Indexer) { idx.blockHashes = repo }
}

// WithMaxRetainedHistory overrides DefaultMaxRetainedHistory.
func WithMaxRetainedHistory(blocks uint64) Option {
	return func(idx *Indexer) { idx.maxHistory = blocks }
}

// WithChunkCommitter groups events, hashes, pruning and checkpoint advance through
// CommitChunk. Rollbacks/resets use RewindChunk; both check checkpoint versions.
// Without it, separate writes require one writer.
// An unresolved deep reorg sets ReconciliationRequired through plain Set, outside
// version checking, to pause even after concurrent checkpoint changes.
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

// WithDeadLetterQueue records decode failures and continues scanning. Without it, one
// decode failure aborts Poll, delaying later logs in the response; see
// handleDecodeFailure.
func WithDeadLetterQueue(repo repository.DeadLetterRepository) Option {
	return func(idx *Indexer) { idx.dlq = repo }
}

// withSleepFunc overrides the backoff wait function, for tests
func withSleepFunc(sleep func(time.Duration)) Option {
	return func(idx *Indexer) { idx.sleep = sleep }
}

// New configures chainID, addresses and startBlock for initial scans and trusted
// rebuilds. Choose a trusted deployment baseline: recovery may rebuild from it within
// policy. A nil decoder names raw data by topic0.
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

// Poll checks checkpoint hash/head regression for reorgs, then scans toward the
// observed head in bounded chunks. Duplicate events are skipped, allowing repeated
// timer calls.
// A checkpoint flagged ReconciliationRequired returns ErrReconciliationRequired before
// chain reads; ResetToTrustedCheckpoint clears the pause.
func (idx *Indexer) Poll(ctx context.Context) error {
	cp, existed, err := idx.loadCheckpoint(ctx)
	if err != nil {
		return err
	}
	if existed && cp.ReconciliationRequired {
		return fmt.Errorf("%w: chain %d paused at block %d, call ResetToTrustedCheckpoint after out-of-band verification", ErrReconciliationRequired, idx.chainID, cp.LastBlock)
	}

	// Read head first so regression below cp.LastBlock triggers rollback rather than
	// repeated HeaderByNumber failures against a missing height
	currentBlock, err := idx.source.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("indexer: BlockNumber: %w", err)
	}

	if existed && cp.LastBlock > 0 && cp.LastBlockHash != "" {
		if currentBlock < cp.LastBlock {
			// The checkpoint exceeds this source's head; search for an ancestor from the lower
			// head
			cp, err = idx.rollback(ctx, cp, currentBlock)
			if err != nil {
				// A competing replica moved the checkpoint; stop cleanly and recompute rollback on
				// the next poll
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

	// Persist a fresh baseline even without new scan work, giving later polls a checkpoint
	// hash to compare
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

	// Validate and commit bounded chunks to catch reorgs racing the scan, beyond Poll's
	// initial checkpoint check. scanAndCommitChunk compares boundary headers and per-log
	// canonical hashes, retrying mismatches before ingestion. Chunking bounds memory and
	// lets backfills resume after the last committed chunk.
	for fromBlock <= currentBlock {
		chunkTo := currentBlock
		if idx.maxLogRange > 0 && fromBlock+idx.maxLogRange-1 < currentBlock {
			chunkTo = fromBlock + idx.maxLogRange - 1
		}
		nextFrom, err := idx.scanAndCommitChunk(ctx, fromBlock, chunkTo, currentBlock, cp)
		if err != nil {
			// Stop after a checkpoint-version conflict; the next Poll reads the winning
			// replica's progress
			if errors.Is(err, errCheckpointVersionConflict) {
				return nil
			}
			return err
		}
		fromBlock = nextFrom
	}
	return nil
}

// errCheckpointVersionConflict marks a lost commit/rewind race. Poll treats it as a
// clean stop rather than returning it.
var errCheckpointVersionConflict = errors.New("indexer: checkpoint advanced by a concurrent writer")

// scanAndCommitChunk checks boundary headers and log BlockHashes before commitChunk
// stores the accepted range. Mismatches retry that bounded chunk. currentBlock anchors
// retention to this poll's head, rather than the possibly lower actual chunk end.
// Return the committed upper bound+1.
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

		// Recheck scanChunk's actual upper boundary for reorgs; range shrinking may make it
		// lower than wantTo
		afterHeader, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(actualTo))
		if err != nil {
			return 0, fmt.Errorf("indexer: fetch chunk boundary header at %d: %w", actualTo, err)
		}
		valid := actualTo == wantTo && afterHeader.Hash() == beforeHeader.Hash()
		// Shrinking changed the boundary, so before/after hashes are incomparable. Rely on
		// per-log canonical validation for this attempt.
		if actualTo != wantTo {
			valid = true
		}

		var validated []types.Log
		var headerCache map[uint64]common.Hash
		if valid {
			// Check each log's canonical BlockHash to detect interior changes even when the
			// boundary hash matches
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

		// Discard mismatched chunks before ingestion and retry the same range with bounded
		// backoff
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

// validateLogsAgainstCanonical drops Removed logs and checks remaining BlockHashes
// against canonical headers. A mismatch returns ok=false, discarding the whole attempt
// to avoid mixing forks. On success, return accepted logs and a header cache seeded by
// toHash for commitChunk.
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

// commitChunk stores decoded events, retained block hashes, pruning and (to, toHash)
// checkpoint progress, reusing headerCache. currentBlock anchors retention to the
// poll's head.
// With a committer, CommitChunk checks cp.Version and updates cp in place on success.
// Without it, writes are separate and require one writer.
func (idx *Indexer) commitChunk(ctx context.Context, from, to, currentBlock uint64, toHash string, logs []types.Log, headerCache map[uint64]common.Hash, cp *models.IndexerCheckpoint) error {
	// Decode and write DLQ failures before the chunk transaction. Existing events are
	// skipped; only new rows enter the commit.
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

// prepareChunkEvents skips stored logs and decodes new ones into ChainEvents. Decode
// failures follow DLQ policy outside the chunk transaction; return only new rows to
// persist.
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

// scanChunk retries FilterLogs for [from,to], returning logs and the actual upper bound
// after any size/timeout shrink
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
		// Shrink on timeouts as well as size errors; repeating an overloaded span may exhaust
		// retries
		if (isRangeTooLargeError(err) || isTimeoutError(err)) && to > from {
			// Halve the span and retry immediately without backoff; the loop still increments
			// attempt
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

// isRangeTooLargeError recognizes provider range/response-size errors by common message
// fragments, allowing range shrinking without a shared error code
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

// isTimeoutError enables range shrinking for context.DeadlineExceeded, net.Error
// timeouts and common timeout wording, including "context canceled"
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

// loadCheckpoint distinguishes an absent first-poll checkpoint from one explicitly
// stored at genesis
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

// handleDecodeFailure records to the DLQ and continues after success; without a DLQ, or
// if recording fails, it aborts Poll
func (idx *Indexer) handleDecodeFailure(ctx context.Context, log types.Log, cause error) error {
	if idx.dlq == nil {
		return fmt.Errorf("indexer: decode log %s#%d: %w", log.TxHash.Hex(), log.Index, cause)
	}
	if err := idx.recordDeadLetter(ctx, log, cause); err != nil {
		return err
	}
	// Successful DLQ recording defers the event for operator retry while allowing
	// checkpoint advance
	return nil
}

// deadLetterID consolidates repeat failures into one entry with increasing RetryCount
func deadLetterID(chainID int64, log types.Log) string {
	return fmt.Sprintf("%d:%s:%d", chainID, log.TxHash.Hex(), log.Index)
}

// recordDeadLetter retains the full log and failure for retry without refetching
// potentially reorged log bytes
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

// RetryDeadLetter reuses retained log bytes after checking the canonical block hash.
// Success ingests and resolves; orphaned logs are dismissed without ingestion. Repeated
// decode failure updates the DLQ and returns the recording result, nil if recording
// succeeds.
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

	// Check the retained BlockHash before retrying old entries, dismissing orphaned logs
	// instead of reintroducing abandoned-fork events
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

// rollback finds an ancestor, deletes watched events after it and rewinds the
// checkpoint with its fresh hash. searchFrom is cp.LastBlock after a hash mismatch, or
// the lower head after regression.
// If recovery needs reconciliation, keep events and flag the checkpoint to pause future
// polls instead of trusting an unverified baseline.
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

// rewind deletes events from deleteFrom and stores newCp. A chunk committer makes both
// version-checked; conflicts return errCheckpointVersionConflict for Poll to stop and
// re-read. Separate-write fallback requires one writer.
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

// findCommonAncestor compares retained and fresh hashes above max(startBlock,
// cp.LastBlock-maxAutoReorgDepth). Reaching the trusted-start floor permits rebuilding;
// reaching the depth floor first requires reconciliation. Without hash history, use
// fixed-window rollback.
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
	// Only the trusted-start floor permits rebuilding without a matched ancestor; a higher
	// depth floor requires reconciliation
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
		// Missing retained hash or fork mismatch: continue searching earlier heights
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

// rollbackToFixedWindow re-baselines from searchFrom minus rollbackWindow, floored at
// startBlock. It needs no hash history; see DefaultRollbackWindow for the deep-fork
// limitation.
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

// ResetToTrustedCheckpoint sets an operator-verified baseline, deletes watched events
// after trustedBlock and clears ReconciliationRequired. Nonzero blocks get a fresh
// chain hash. It can also force resync when no pause is set; verify the block out of
// band first.
func (idx *Indexer) ResetToTrustedCheckpoint(ctx context.Context, trustedBlock uint64) error {
	var hash string
	if trustedBlock > 0 {
		header, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(trustedBlock))
		if err != nil {
			return fmt.Errorf("indexer: fetch trusted checkpoint header: %w", err)
		}
		hash = header.Hash().Hex()
	}
	// Version-check this reset against the loaded checkpoint, treating absence as 0. A
	// competing advance must make reset fail instead of stranding its events above the
	// rewind.
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
