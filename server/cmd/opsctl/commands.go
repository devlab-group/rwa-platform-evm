package main

// Command implementations: the audited operator commands for checkpoint
// reset, DLQ inspect/retry/dismiss, replication retry, local restore, and
// tx replace. Every function here is deliberately shaped
// to take already-constructed dependencies (a repository.Repositories, an
// *indexer.Indexer, a blockchain.TxManager, an *ipfs.ReplicationManager) and
// write human-readable progress to an io.Writer, rather than reading flags
// or touching os.Stdout/os.Exit directly — main.go owns process wiring
// (config, Mongo/chain connections, flag parsing) so these can be smoke
// tested directly against in-memory repos and fakes (commands_test.go), the
// same split cmd/reindex's snapshotCounts uses for its one helper.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
)

// blockNumberBig converts a block number to the *big.Int HeaderByNumber
// expects.
func blockNumberBig(n uint64) *big.Int { return new(big.Int).SetUint64(n) }

// opsCtx bundles what every command needs to run and to audit-log its own
// invocation.
type opsCtx struct {
	ctx   context.Context
	repos *repository.Repositories
	audit *auditlog.Logger
	// actor is the attributable audit label for this invocation (main.go's
	// auditActor sets it to "opsctl:"+the --actor flag or OS user). opsctl is
	// not credential-gated, so this is for attribution only, not authorization.
	actor string
	out   io.Writer
}

// audited runs op (the actual mutation) wrapped in the
// intent-before-effect / fail-closed pattern: it first persists a durable
// audit INTENT and, if that cannot be made durable, REFUSES to run op at all
// (a privileged chain/checkpoint/DLQ/replication mutation with no durable
// actor/action trail is worse than not running it). After op it records a
// completion linked to that intent, capturing success/failure — a failure to
// write the RESULT is only a warning, since the intent already durably
// captured who invoked what. This supersedes the old "run op, then best-effort
// append one entry" flow, which recorded nothing until AFTER the mutation and
// only warned on an audit failure (fail-open).
func (o opsCtx) audited(action, target string, metadata map[string]any, op func() error) error {
	intentID, err := o.audit.RecordIntent(o.ctx, "opsctl", o.actor, action, target, metadata)
	if err != nil {
		fmt.Fprintf(o.out, "ERROR: refusing to run %s %s — its audit intent could not be made durable: %v\n", action, target, err)
		return fmt.Errorf("opsctl: audit intent for %s %s could not be persisted; refusing to proceed: %w", action, target, err)
	}

	opErr := op()

	resultMeta := map[string]any{}
	if opErr != nil {
		resultMeta["error"] = opErr.Error()
	}
	if auditErr := o.audit.RecordResult(o.ctx, intentID, "opsctl", o.actor, action, target, opErr == nil, resultMeta); auditErr != nil {
		fmt.Fprintf(o.out, "WARNING: audit result write failed for %s %s: %v\n", action, target, auditErr)
	}
	return opErr
}

// --- tx replace ---

func runTxReplace(o opsCtx, txs blockchain.TxManager, signer blockchain.Signer, id string, bumpPercent int64) error {
	return o.audited("tx.replace", id, map[string]any{"bumpPercent": bumpPercent}, func() error {
		tx, err := txs.Replace(o.ctx, id, signer, bumpPercent)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.out, "replaced %s: new txHash=%s status=%s\n", id, tx.TxHash, tx.Status)
		return nil
	})
}

// --- indexer checkpoint reset ---

// errTrustedHashMismatch is returned (and the checkpoint left untouched)
// when the operator-supplied trusted hash disagrees with what the chain
// source itself reports at trustedBlock. Requiring an explicit trusted
// block/hash for the reorg-recovery reset is a SECOND, independent check on
// top of what Indexer.ResetToTrustedCheckpoint already does (it derives the
// hash from the source itself): the operator
// must state, from their own independent source of truth (a block
// explorer, another node), what they believe the correct hash is, and this
// command refuses to proceed — rather than silently trusting whatever the
// configured RPC endpoint happens to return — if that live answer disagrees.
var errTrustedHashMismatch = errors.New("opsctl: trusted hash does not match the chain source's header at that block")

func runIndexerResetCheckpoint(o opsCtx, idx *indexer.Indexer, source indexer.ChainSource, trustedBlock uint64, trustedHashHex string) error {
	return o.audited("indexer.resetToTrustedCheckpoint", fmt.Sprintf("block:%d", trustedBlock),
		map[string]any{"trustedBlock": trustedBlock, "trustedHash": trustedHashHex}, func() error {
			header, err := source.HeaderByNumber(o.ctx, blockNumberBig(trustedBlock))
			if err != nil {
				return fmt.Errorf("fetch header at %d to verify operator-supplied hash: %w", trustedBlock, err)
			}
			if header.Hash().Hex() != trustedHashHex {
				return fmt.Errorf("%w: source reports %s, operator supplied %s", errTrustedHashMismatch, header.Hash().Hex(), trustedHashHex)
			}
			if err := idx.ResetToTrustedCheckpoint(o.ctx, trustedBlock); err != nil {
				return err
			}
			fmt.Fprintf(o.out, "checkpoint reset to block %d (hash %s); every event after it was deleted\n", trustedBlock, trustedHashHex)
			return nil
		})
}

// --- dead letter queue ---

func runDLQList(o opsCtx) error {
	entries, err := o.repos.IndexerDeadLetters.List(o.ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.out, "%d dead letter entries:\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(o.out, "  %s  block=%d tx=%s category=%s retries=%d resolved=%v\n",
			e.ID, e.BlockNumber, e.TxHash, e.ErrorCategory, e.RetryCount, e.Resolved)
	}
	return nil
}

func runDLQInspect(o opsCtx, id string) error {
	e, err := o.repos.IndexerDeadLetters.Get(o.ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.out, "id=%s chainId=%d block=%d blockHash=%s tx=%s logIndex=%d\n", e.ID, e.ChainID, e.BlockNumber, e.BlockHash, e.TxHash, e.LogIndex)
	fmt.Fprintf(o.out, "eventSignature=%s category=%s retries=%d resolved=%v\n", e.EventSignature, e.ErrorCategory, e.RetryCount, e.Resolved)
	fmt.Fprintf(o.out, "firstFailedAt=%s lastFailedAt=%s\n", e.FirstFailedAt, e.LastFailedAt)
	fmt.Fprintf(o.out, "error: %s\n", e.ErrorMessage)
	return nil
}

func runDLQRetry(o opsCtx, idx *indexer.Indexer, id string) error {
	return o.audited("dlq.retry", id, nil, func() error {
		if err := idx.RetryDeadLetter(o.ctx, id); err != nil {
			return err
		}
		e, err := o.repos.IndexerDeadLetters.Get(o.ctx, id)
		if err != nil {
			return err
		}
		if e.Resolved {
			fmt.Fprintf(o.out, "%s resolved (category=%s)\n", id, e.ErrorCategory)
		} else {
			fmt.Fprintf(o.out, "%s still unresolved after retry (category=%s): %s\n", id, e.ErrorCategory, e.ErrorMessage)
		}
		return nil
	})
}

func runDLQDismiss(o opsCtx, id string) error {
	return o.audited("dlq.dismiss", id, nil, func() error {
		if err := o.repos.IndexerDeadLetters.Resolve(o.ctx, id); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "%s dismissed (marked resolved without ingesting)\n", id)
		return nil
	})
}

// --- ipfs replication ---

func runIPFSRetry(o opsCtx, rm *ipfs.ReplicationManager, id string, data []byte) error {
	return o.audited("ipfs.retry", id, map[string]any{"bytes": len(data)}, func() error {
		if err := rm.Retry(o.ctx, id, data); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "%s: replication retry complete\n", id)
		return nil
	})
}

func runIPFSRestoreLocal(o opsCtx, rm *ipfs.ReplicationManager, id string) error {
	return o.audited("ipfs.restoreLocal", id, nil, func() error {
		if err := rm.RestoreLocal(o.ctx, id); err != nil {
			return err
		}
		fmt.Fprintf(o.out, "%s: local copy restored from a backup destination\n", id)
		return nil
	})
}
