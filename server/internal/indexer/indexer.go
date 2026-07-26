// Package indexer ingests contract event logs into repository.ChainEvent
// records keyed by (chainId, address, txHash, logIndex), tracks a scan
// checkpoint, and rolls back on reorg. Redemption and
// other read models are reconstructed only from these persisted events,
// never overridden by server workflow state.
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

// CheckpointAddress is the synthetic IndexerCheckpoint.Address used because
// this indexer scans every configured project contract address in one
// combined FilterLogs query and keeps one shared scan position for them,
// rather than one checkpoint per address.
const CheckpointAddress = "ALL"

// DefaultRollbackWindow is how many blocks a detected reorg rolls back by
// default. A single-block rollback with an immediate re-fetch of "the new
// tip" cannot distinguish "this block is stable" from "this block was also
// replaced but happens to look stable right now" within one Poll call —
// only time-separated polls can. Rolling back a conservative window
// (rather than bisecting to the exact fork point) trades a bit of
// re-scanning for correctness: even a reorg deeper than the window is
// caught by the *next* Poll, since the checkpoint hash comparison is
// against a value persisted from a genuinely earlier point in time.
const DefaultRollbackWindow = 10

// ChainSource is the subset of blockchain.Client the indexer needs. It is
// satisfied by blockchain.Client and by FakeSource in tests, so indexer
// logic (in particular reorg rollback) is unit-testable without a live chain.
type ChainSource interface {
	BlockNumber(ctx context.Context) (uint64, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// EventDecoder turns a raw log into a named event with decoded fields. The
// default decoder (used when nil) only extracts topic0 as the event name,
// which is enough to prove ingestion/rollback correctness in tests; real
// deployments should supply a decoder built from internal/bindings so
// higher-level read models (redemption status, compliance, sales) can be
// derived from the Name/Data fields.
type EventDecoder func(log types.Log) (name string, data map[string]any, err error)

// DefaultMaxRetainedHistory bounds how many blocks of per-block hash
// history (see repository.BlockHashRepository) the indexer keeps to walk
// back to a true common ancestor on a reorg deeper than rollbackWindow.
// Well beyond any plausible reorg depth on a chain this platform targets,
// while staying a small, bounded amount of storage.
const DefaultMaxRetainedHistory = 256

// DefaultMaxAutoReorgDepth bounds how deep a reorg the indexer will resolve
// automatically. A common ancestor found within this many blocks of the
// checkpoint is trusted and rolled back to as usual; a divergence that can't
// be verified within this depth is NOT silently trusted — Poll instead
// returns ErrReconciliationRequired and stops advancing until an operator
// calls
// ResetToTrustedCheckpoint. Deliberately independent of maxHistory: history
// retention is a storage bound, this is a policy bound on how much
// automatic, unattended rollback is acceptable before a human should look.
const DefaultMaxAutoReorgDepth = 100

// ErrReconciliationRequired is returned by Poll (and wraps rollback's
// internal error) when a detected reorg's common ancestor cannot be
// verified within maxAutoReorgDepth blocks of the checkpoint. Rather than
// trusting a freshly-read hash at an unverified point — which would blindly
// treat the oldest retained height's hash as a new baseline — the indexer
// leaves all persisted state
// exactly as it was and stops advancing (Poll keeps returning this error on
// every subsequent call) until ResetToTrustedCheckpoint is called with a
// block the operator has verified out of band.
var ErrReconciliationRequired = errors.New("indexer: reorg exceeds automatic recovery policy; reconciliation required")

// DefaultMaxLogRange bounds how many blocks a single FilterLogs call spans.
// Many RPC providers limit the number of blocks allowed in a single
// eth_getLogs request, so the range is configurable. A large-but-bounded
// default: small enough
// to stay well under every mainstream provider's documented range cap,
// large enough that a routine 5s poll on a live chain is still one request
// in the common case. 0 means unbounded (a single request spanning the
// whole [from,to]) — an explicit opt-out for a provider known to have no
// range limit, not the default.
const DefaultMaxLogRange = 2000

// DefaultMaxRangeRetries bounds how many times scanLogs retries a chunk
// against a transient (non-range) FilterLogs failure before giving up and
// returning the error to Poll's caller, using bounded exponential backoff.
// Failures classified as "range too
// large" are NOT counted against this budget — see scanChunk's doc comment.
const DefaultMaxRangeRetries = 5

// initialRangeBackoff/maxRangeBackoff bound the exponential backoff between
// transient-failure retries within scanChunk.
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
	// sleep backs scanChunk's backoff wait; overridden in tests (see
	// withSleepFunc) so retry coverage runs instantly instead of actually
	// waiting out the backoff schedule.
	sleep func(time.Duration)
}

// Option configures optional Indexer behavior.
type Option func(*Indexer)

// WithRollbackWindow overrides DefaultRollbackWindow.
func WithRollbackWindow(blocks uint64) Option {
	return func(idx *Indexer) { idx.rollbackWindow = blocks }
}

// WithBlockHashRepository enables deep-reorg protection: every
// successful Poll records the scanned head's hash, and a detected reorg
// walks backward through that retained history to find a true common
// ancestor instead of blindly rolling back a fixed window and re-baselining
// unconditionally (which cannot tell "the fork is within this window" from
// "the fork is deeper," silently leaving stale events below the window with
// a checkpoint that, freshly re-read, now matches canonical — so the
// divergence is never rediscovered). Without this option, rollback falls
// back to the simpler fixed-window behavior.
func WithBlockHashRepository(repo repository.BlockHashRepository) Option {
	return func(idx *Indexer) { idx.blockHashes = repo }
}

// WithMaxRetainedHistory overrides DefaultMaxRetainedHistory.
func WithMaxRetainedHistory(blocks uint64) Option {
	return func(idx *Indexer) { idx.maxHistory = blocks }
}

// WithChunkCommitter enables the atomic commit path: each scanned chunk's
// validated events, retained block hashes, pruning, and checkpoint advance
// are written through repo.CommitChunk in ONE transaction (majority write
// concern in the mongodb impl), with the checkpoint advance conditional on
// its stored version so two active indexer replicas can never interleave a
// stale checkpoint over a newer one. Backwards checkpoint moves — reorg
// rollback and ResetToTrustedCheckpoint — go through repo.RewindChunk under
// the same fence, so the guarantee covers every checkpoint write, not just
// forward ones. Without this option the indexer falls
// back to the simpler separate-writes commit — an opt-in weaker mode
// (matching WithBlockHashRepository's pattern), still used by unit tests
// that don't exercise the atomic primitive.
//
// One checkpoint write is deliberately left unfenced: flagging
// ReconciliationRequired when a reorg is too deep to resolve automatically
// (see rollback). That write only ever pauses the indexer, so forcing it
// through is the fail-closed choice — a lost race there would leave a diverged
// indexer running instead.
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

// WithDeadLetterQueue enables the DLQ policy: a log this indexer cannot
// decode is recorded to repo instead of aborting Poll (and, without this
// option, blocking every later canonical event behind it forever within the
// same FilterLogs response — see handleDecodeFailure). Without it, a decode
// failure aborts Poll — an opt-in weaker mode, not a silent skip.
func WithDeadLetterQueue(repo repository.DeadLetterRepository) Option {
	return func(idx *Indexer) { idx.dlq = repo }
}

// withSleepFunc overrides the backoff wait function; test-only.
func withSleepFunc(sleep func(time.Duration)) Option {
	return func(idx *Indexer) { idx.sleep = sleep }
}

// New constructs an Indexer for chainID scanning addresses, starting from
// startBlock if no checkpoint exists yet. startBlock doubles as the
// indexer's trusted-start/deployment block: the automatic common-ancestor
// walk-back never needs to go below it, since no event this
// indexer could have persisted predates it, so rebuilding from startBlock
// is always a safe, verified baseline regardless of maxAutoReorgDepth. A
// nil decoder falls back to a topic0-only decode.
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

// Poll performs one scan cycle: detect and roll back a reorg if the
// checkpointed block's hash no longer matches the chain, then ingest any
// new logs up to the current head, ONE bounded chunk at a time. It is safe
// to call repeatedly (e.g. on a timer); ingestion is idempotent via
// ChainEventRepository.Exists.
//
// If a prior Poll left the checkpoint in ReconciliationRequired state
// (a reorg deeper than the automatic-recovery policy), Poll
// short-circuits and keeps returning ErrReconciliationRequired without
// touching chain state, so derived read models never silently advance past
// an unresolved divergence. Call ResetToTrustedCheckpoint to recover.
func (idx *Indexer) Poll(ctx context.Context) error {
	cp, existed, err := idx.loadCheckpoint(ctx)
	if err != nil {
		return err
	}
	if existed && cp.ReconciliationRequired {
		return fmt.Errorf("%w: chain %d paused at block %d — call ResetToTrustedCheckpoint after out-of-band verification", ErrReconciliationRequired, idx.chainID, cp.LastBlock)
	}

	// Read the current head BEFORE comparing against the checkpoint.
	// Reading the checkpoint's header first meant an RPC whose
	// visible head had regressed below the checkpoint (failover to a
	// lagging node, or a reorg that shortened the canonical chain) would
	// simply fail HeaderByNumber(cp.LastBlock) every poll instead of being
	// recognized and handled as a reorg.
	currentBlock, err := idx.source.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("indexer: BlockNumber: %w", err)
	}

	if existed && cp.LastBlock > 0 && cp.LastBlockHash != "" {
		if currentBlock < cp.LastBlock {
			// Head regression: cp.LastBlock no longer exists on this
			// source at all. Treat it as a reorg and walk back from the
			// new (lower) head instead of erroring.
			cp, err = idx.rollback(ctx, cp, currentBlock)
			if err != nil {
				// Same clean stop the forward commit takes on a lost version
				// race — another replica moved the checkpoint, so this
				// rollback was computed against a state that no longer exists.
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

	// On a fresh (never-checkpointed) indexer we still want to establish a
	// baseline checkpoint even if there happens to be nothing new to scan,
	// so future polls have a real historical hash to compare against
	// instead of silently never persisting anything.
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

	// Scan and commit ONE bounded, hash-validated chunk at a time, instead
	// of accumulating the WHOLE [fromBlock,currentBlock]
	// range into one slice and only checking/persisting a checkpoint at
	// the very end. The old shape let a reorg racing the scan (fork A logs
	// ingested, then the node moves to fork B before the final
	// HeaderByNumber/checkpoint write) permanently commit fork-A events
	// under a fork-B checkpoint hash — the checkpoint-mismatch rollback
	// above only ever catches a reorg that has ALREADY completed by the
	// START of the NEXT Poll call, not one racing the CURRENT call.
	// scanAndCommitChunk closes that window per chunk: it captures the
	// chunk's boundary header before scanning, re-reads it after, and
	// validates every returned log's own BlockHash against the canonical
	// header at its block number — discarding and retrying the whole
	// chunk (never a partial commit) if anything disagrees. Chunking also
	// releases memory between chunks and lets a restart resume from the
	// last successfully committed chunk instead of from fromBlock again on
	// a large backfill.
	for fromBlock <= currentBlock {
		chunkTo := currentBlock
		if idx.maxLogRange > 0 && fromBlock+idx.maxLogRange-1 < currentBlock {
			chunkTo = fromBlock + idx.maxLogRange - 1
		}
		nextFrom, err := idx.scanAndCommitChunk(ctx, fromBlock, chunkTo, currentBlock, cp)
		if err != nil {
			// Another indexer replica advanced the checkpoint out from under
			// this atomic commit. Not an error — stop this
			// poll cleanly; the next poll re-reads the advanced checkpoint and
			// continues from there.
			if errors.Is(err, errCheckpointVersionConflict) {
				return nil
			}
			return err
		}
		fromBlock = nextFrom
	}
	return nil
}

// errCheckpointVersionConflict is returned internally by commitChunk when the
// atomic CommitChunk lost the conditional checkpoint-version race to a
// concurrent indexer replica. Poll treats it as a clean stop,
// never surfaces it to the caller.
var errCheckpointVersionConflict = errors.New("indexer: checkpoint advanced by a concurrent writer")

// scanAndCommitChunk validates [from,wantTo] as a coherent chain snapshot
// and, only if validation succeeds, commits it: ingests every log, records
// block hashes within the retention window, and advances the checkpoint to
// the chunk's actual upper bound — all before returning, so the range's
// events, block hashes, and checkpoint commit together and a failed
// validation has a small retry scope (one bounded chunk at a time).
// currentBlock is the OVERALL chain head this Poll call
// observed (not necessarily this chunk's own upper bound), used only for
// the block-hash retention-window math, matching the pre-chunking behavior
// of retaining history relative to the true head rather than each chunk's
// end during a multi-chunk backfill.
//
// Returns the next fromBlock to scan (the committed chunk's actual upper
// bound + 1).
func (idx *Indexer) scanAndCommitChunk(ctx context.Context, from, wantTo, currentBlock uint64, cp *models.IndexerCheckpoint) (uint64, error) {
	backoff := initialRangeBackoff
	for attempt := 1; ; attempt++ {
		// Capture the boundary hash before scanning.
		beforeHeader, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(wantTo))
		if err != nil {
			return 0, fmt.Errorf("indexer: fetch chunk boundary header at %d: %w", wantTo, err)
		}

		logs, actualTo, err := idx.scanChunk(ctx, from, wantTo)
		if err != nil {
			return 0, err
		}

		// Re-read the boundary after scanning and discard/retry the range if
		// it changed. Re-fetch the ACTUAL upper
		// bound scanChunk covered (which may be below wantTo if an
		// oversized-response/timeout error forced a shrink — see
		// scanChunk), so this check is meaningful even when a shrink
		// happened; when it didn't (actualTo==wantTo), comparing against
		// beforeHeader also catches a reorg that happened during the
		// FilterLogs call itself.
		afterHeader, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(actualTo))
		if err != nil {
			return 0, fmt.Errorf("indexer: fetch chunk boundary header at %d: %w", actualTo, err)
		}
		valid := actualTo == wantTo && afterHeader.Hash() == beforeHeader.Hash()
		// wantTo != actualTo means scanChunk itself already shrank the
		// range (an oversized-response/timeout signal, unrelated to a
		// reorg) — the before/after comparison above isn't meaningful for
		// a boundary we never actually asked scanChunk to hold constant,
		// so treat that case as "boundary check trivially satisfied" and
		// rely entirely on the per-log validation below.
		if actualTo != wantTo {
			valid = true
		}

		var validated []types.Log
		var headerCache map[uint64]common.Hash
		if valid {
			// Validate every returned log's BlockHash against the canonical
			// header at its block number before committing it — catches a
			// reorg inside the range even when the boundary hash happens to
			// (re)converge by `actualTo`.
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

		// The chain moved under us mid-scan (or a log disagreed with the
		// canonical header we just re-read): discard everything from this
		// attempt — nothing was ingested or committed — and retry the
		// SAME requested range fresh, bounded like any other transient
		// failure.
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

// validateLogsAgainstCanonical confirms every non-removed log's BlockHash
// still matches the canonical header at its own block number, returning
// only the logs that pass and ok=true. toHash is already known (the
// chunk's just-re-read upper-bound header) and seeded into the returned
// cache so commitChunk's block-hash recording can reuse it instead of
// re-fetching. A log with a `Removed` flag from the RPC node itself is
// dropped, same as before this change — it is the node's own signal that
// this log's block is no longer canonical, which the explicit hash check
// below would catch anyway, but skipping it avoids one redundant
// HeaderByNumber call.
//
// If ANY log disagrees, ok=false and the caller discards this entire
// attempt (see scanAndCommitChunk) rather than committing the logs that
// DID match: a disagreement means a reorg may still be in progress across
// the range, and partially trusting it risks exactly the mixed-fork commit
// this validation exists to prevent.
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

// commitChunk persists everything scanAndCommitChunk validated for
// [from,to]: the decoded events, block hashes within the retention window
// (reusing headerCache where possible), history pruning, and the checkpoint
// advance to (to, toHash).
//
// When a ChainChunkRepository is configured (WithChunkCommitter), all of
// those land through ONE atomic CommitChunk (a Mongo transaction with
// majority write concern), and the checkpoint advance is conditional on
// cp.Version so a concurrent replica can't interleave a stale checkpoint. cp
// is advanced in place on success. Without a committer, it falls back to
// separate writes (still correct for a single-writer deployment; the
// atomicity guarantee only holds with the committer wired). currentBlock
// (the overall poll head, not this chunk's
// `to`) anchors the retention-window math exactly as before.
func (idx *Indexer) commitChunk(ctx context.Context, from, to, currentBlock uint64, toHash string, logs []types.Log, headerCache map[uint64]common.Hash, cp *models.IndexerCheckpoint) error {
	// Decode (and DLQ any failures) BEFORE the atomic commit — a decode
	// failure is recorded to the DLQ out-of-band and must not be part of the
	// transactional event set. Already-ingested events are skipped so the
	// commit only carries genuinely new rows.
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

	// Fallback (no atomic committer): separate writes.
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

// prepareChunkEvents decodes every not-already-ingested log into a
// models.ChainEvent, routing a decode failure to the DLQ out of band so it
// never becomes part of an atomic chunk commit.
// An already-existing (idempotent) event is skipped so the returned set is
// exactly the new rows to persist.
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

// scanChunk fetches [from,to] with retry, returning the logs and the
// highest block number ACTUALLY covered by the successful request (equal to
// to unless an oversized-response or timeout error forced a shrink first).
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
		// Shrink ranges on deadline/timeouts as well as explicit provider
		// size errors. isRangeTooLargeError alone only recognized
		// oversized-response wording; an ordinary provider deadline/timeout
		// retried the SAME span until the retry budget was exhausted instead
		// of narrowing it.
		if (isRangeTooLargeError(err) || isTimeoutError(err)) && to > from {
			// Deterministic signal, not a transient failure: shrink and
			// retry the SAME fromBlock immediately, without spending a
			// backoff-retry attempt on it.
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

// isRangeTooLargeError classifies a FilterLogs error as an RPC provider
// rejecting the requested range/response size outright, so the query range
// can be reduced after oversized responses. Matches common provider error
// text (Alchemy/Infura/QuickNode/geth-family
// nodes all phrase this differently; there is no standardized JSON-RPC
// error code for it) rather than a single fixed string.
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

// isTimeoutError classifies a FilterLogs error as a deadline/timeout, so
// the range gets shrunk on timeouts too, not only explicit provider size
// errors. isRangeTooLargeError alone missed this, so an ordinary provider
// timeout just retried the same span with backoff until the retry budget
// ran out instead of narrowing it — narrowing is far more likely to
// eventually succeed against a provider whose timeout is itself a function
// of response size. Checks both the
// structured net.Error/context signals and common provider wording, same
// approach as isRangeTooLargeError.
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

// loadCheckpoint returns the persisted checkpoint and whether one existed.
// A missing checkpoint (first-ever poll) is distinct from a checkpoint that
// happens to equal the zero value, so Poll can tell "nothing persisted yet"
// apart from "persisted at genesis".
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

// buildEvent constructs the ChainEvent for a successfully decoded log,
// shared by prepareChunkEvents and RetryDeadLetter.
func buildEvent(chainID int64, log types.Log, name string, data map[string]any) *models.ChainEvent {
	return &models.ChainEvent{
		ChainID: chainID, Address: log.Address.Hex(), TxHash: log.TxHash.Hex(), LogIndex: log.Index,
		BlockNumber: log.BlockNumber, BlockHash: log.BlockHash.Hex(), Name: name, Data: data,
		IndexedAt: time.Now().UTC(),
	}
}

// handleDecodeFailure implements the DLQ policy: an unprocessable event is
// persisted to a DLQ rather than discarded, without silently skipping
// canonical events. See WithDeadLetterQueue's doc comment for the
// no-DLQ-configured fallback.
func (idx *Indexer) handleDecodeFailure(ctx context.Context, log types.Log, cause error) error {
	if idx.dlq == nil {
		return fmt.Errorf("indexer: decode log %s#%d: %w", log.TxHash.Hex(), log.Index, cause)
	}
	if err := idx.recordDeadLetter(ctx, log, cause); err != nil {
		return err
	}
	// Swallowed: recorded for operator triage via RetryDeadLetter. Poll
	// continues scanning past this log and the checkpoint still advances —
	// exactly what "without silently skipping canonical events" requires:
	// the event is not lost, just deferred and made visible.
	return nil
}

// deadLetterID deterministically identifies one failed log, so a repeated
// failure across polls updates one DeadLetterRepository entry (bumping
// RetryCount) instead of growing the queue unboundedly.
func deadLetterID(chainID int64, log types.Log) string {
	return fmt.Sprintf("%d:%s:%d", chainID, log.TxHash.Hex(), log.Index)
}

// recordDeadLetter persists log's failure (and its full source data, so an
// operator retry never needs to re-query a chain that may have since
// reorged past it) to the DLQ.
func (idx *Indexer) recordDeadLetter(ctx context.Context, log types.Log, cause error) error {
	sig := ""
	if len(log.Topics) > 0 {
		sig = log.Topics[0].Hex()
	}
	raw, merr := json.Marshal(log)
	if merr != nil {
		raw = nil // best-effort: still record the failure itself even if the log won't round-trip
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

// RetryDeadLetter re-attempts decoding and ingesting a DLQ entry's retained
// source log, so operators can retry DLQ entries. On success the entry is
// marked Resolved; on a repeat decode
// failure it is re-recorded (bumping RetryCount) and the error is returned
// so the caller knows the retry did not succeed.
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

	// Before a DLQ replay, fetch the canonical header and require
	// header.Hash == retainedLog.BlockHash; resolve an orphan as dismissed
	// rather than ingesting it. A DLQ entry can sit unresolved
	// for an arbitrary time; by the time an operator retries it, the block
	// it was retained from may no longer be canonical at all (a reorg the
	// indexer already rolled back past this height, well before it ever
	// reached this specific log). Ingesting it anyway would silently
	// reintroduce exactly the orphaned-fork-event problem the normal scan
	// path guards against.
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
		return idx.dlq.Resolve(ctx, id) // dismissed, not ingested — this log is correctly gone, not a failure to fix
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

// rollback discards every event at/after the discovered common ancestor's
// next block for every monitored address, and moves the checkpoint back to
// that ancestor, re-baselining LastBlockHash from a fresh chain read.
// searchFrom is the highest block the ancestor search may consider — it's
// cp.LastBlock for a normal hash-mismatch reorg, or the (lower) current
// head on a detected head regression, since cp.LastBlock may not even exist
// on the source in that case.
//
// If findCommonAncestor reports ErrReconciliationRequired, rollback does
// NOT delete any events or move the checkpoint forward — it only flips
// ReconciliationRequired on the persisted checkpoint so every subsequent
// Poll short-circuits on the same unresolved state instead of silently
// trusting an unverified point (see findCommonAncestor).
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

// rewind applies a backwards checkpoint move — a reorg rollback or an
// operator's trusted reset — deleting every event at or above deleteFrom and
// storing newCp.
//
// In atomic mode the two happen in one version-fenced transaction, for exactly
// the reason the forward commit is fenced: a concurrent forward commit that
// lands between the delete and the checkpoint write would otherwise survive the
// unconditional rewind, its fork events stranded above a checkpoint low enough
// that no later rollback ever reaches them. Losing the fence surfaces as
// errCheckpointVersionConflict, which Poll treats as a clean stop — the next
// poll re-reads the advanced checkpoint and re-detects the reorg from there.
//
// Without a chunk committer this falls back to the original separate writes,
// which is the single-writer behavior and identical in that topology.
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

	// Fallback (no atomic committer): separate writes.
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

// findCommonAncestor locates the highest block number at/below searchFrom
// whose retained hash still matches a fresh chain read. A single fixed
// rollbackWindow can't distinguish "the fork is within this window" from
// "the fork is deeper", which is why the retained history is walked instead.
//
// The walk-back stops at policyFloor := max(startBlock, cp.LastBlock -
// maxAutoReorgDepth). Reaching startBlock (the trusted-start/deployment
// block — see New's doc comment) without a verified match is always safe
// to auto-rebuild from: nothing this indexer could have persisted predates
// it, so there is nothing to lose. Reaching maxAutoReorgDepth instead
// (i.e. policyFloor == cp.LastBlock-maxAutoReorgDepth and that's deeper
// into history than startBlock) means the divergence could extend
// arbitrarily further back than we've verified, so do NOT silently trust a
// freshly-read hash at that point; return ErrReconciliationRequired instead.
//
// Without a configured BlockHashRepository (WithBlockHashRepository), this
// falls back to the fixed-window behavior — an opt-in weaker mode, not a
// silent default.
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
	// autoRecoverable is decided by which floor dominates, not by where the
	// search happens to start: reaching the trusted-start block is always
	// safe (it's the deployment boundary, not a depth cutoff), reaching the
	// depth cutoff before that is not.
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
		// err was ErrNotFound (no retained hash at this height) or the
		// stored hash didn't match (still inside the fork): keep walking.
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

// rollbackToFixedWindow is the simpler behavior, kept as a fallback for
// Indexers constructed without WithBlockHashRepository: roll back a fixed
// window (from searchFrom, not necessarily cp.LastBlock — see rollback's
// doc comment on head regression) and re-baseline unconditionally, never
// below idx.startBlock. See DefaultRollbackWindow and findCommonAncestor's
// doc comment for this fallback's known weaker-mode limitation.
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

// ResetToTrustedCheckpoint recovers from ErrReconciliationRequired by
// designating trustedBlock — verified out of band (e.g. against a block
// explorer or another trusted node) — as the new baseline. It
// rolls back every monitored address's events at/after trustedBlock+1
// exactly like an automatically discovered common ancestor, re-baselines
// the checkpoint at trustedBlock with a freshly read hash, and clears
// ReconciliationRequired so Poll resumes. Safe to call even when the
// indexer isn't currently flagged (e.g. an operator wants to force a
// resync); it always re-derives trustedBlock's hash fresh rather than
// trusting a caller-supplied one.
func (idx *Indexer) ResetToTrustedCheckpoint(ctx context.Context, trustedBlock uint64) error {
	var hash string
	if trustedBlock > 0 {
		header, err := idx.source.HeaderByNumber(ctx, new(big.Int).SetUint64(trustedBlock))
		if err != nil {
			return fmt.Errorf("indexer: fetch trusted checkpoint header: %w", err)
		}
		hash = header.Hash().Hex()
	}
	// The rewind is fenced on the checkpoint version this reset was computed
	// against, so a running replica that advances the checkpoint mid-reset
	// makes the reset fail loudly rather than silently stranding that
	// replica's events above a rewound checkpoint. An absent checkpoint reads
	// as version 0 — the same baseline a fresh indexer's first commit uses.
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
