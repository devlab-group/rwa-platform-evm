package main

// Command implementations for checkpoint reset, DLQ inspect, retry and dismiss,
// replication retry, local restore and tx replace. Each takes constructed
// dependencies and writes progress to an io.Writer instead of reading flags or
// using os.Stdout, so tests can run them against in-memory repositories and
// fakes. main.go owns config, connections and flag parsing.

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

// blockNumberBig converts a block number to the *big.Int HeaderByNumber takes
func blockNumberBig(n uint64) *big.Int { return new(big.Int).SetUint64(n) }

// opsCtx bundles what a command needs to run and to write its audit entries
type opsCtx struct {
	ctx   context.Context
	repos *repository.Repositories
	audit *auditlog.Logger
	// actor labels audit entries, as set by auditActor, and does not authorize anything
	actor string
	out   io.Writer
}

// audited runs op between a durable audit intent and a result entry. If the
// intent cannot be persisted, op does not run, because a privileged mutation
// with no durable record of who invoked it is worse than not running it. A
// failure to write the result entry is only a warning, since the intent already
// records the invocation.
func (o opsCtx) audited(action, target string, metadata map[string]any, op func() error) error {
	intentID, err := o.audit.RecordIntent(o.ctx, "opsctl", o.actor, action, target, metadata)
	if err != nil {
		fmt.Fprintf(o.out, "ERROR: refusing to run %s %s - its audit intent could not be made durable: %v\n", action, target, err)
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

// errTrustedHashMismatch means the hash the operator supplied differs from the
// chain source's header hash at trustedBlock, and the checkpoint is left
// untouched. Indexer.ResetToTrustedCheckpoint reads the hash from the source
// itself, so this check against a hash the operator verified elsewhere, such as
// on a block explorer or another node, stops the reset from trusting the
// configured RPC endpoint alone.
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
