package main

// Recovery commands take dependencies and an io.Writer for testing with memory stores
// and fakes. main.go owns configuration, connections and flags. Commands handle
// transaction replacement, checkpoint reset, DLQ list/inspect/retry/dismiss, IPFS
// retry/restore and the asset profile check.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
)

// blockNumberBig converts a block number to the *big.Int HeaderByNumber takes
func blockNumberBig(n uint64) *big.Int { return new(big.Int).SetUint64(n) }

// opsCtx holds command dependencies and audit context
type opsCtx struct {
	ctx   context.Context
	repos *repository.Repositories
	audit *auditlog.Logger
	// actor attributes audit entries without authorizing actions (auditActor)
	actor string
	out   io.Writer
}

// audited requires a durable intent before op, then attempts a linked result write. A
// failed result write warns and preserves op's outcome.
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

// Transaction replacement

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

// Indexer checkpoint reset

// errTrustedHashMismatch rejects a reset before changing the checkpoint when the RPC
// header differs from the operator's trusted hash. Verify that hash independently
// (another node or explorer), since ResetToTrustedCheckpoint fetches its hash from the
// configured RPC.
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

// Dead-letter queue

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

// IPFS replication

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

// Asset profile check

// runProfileCheck validates the stored profile and compares its recomputed and stored digests.
// Record creation revalidates the profile, whose digest is fixed on-chain, so run this
// read-only check before upgrading to detect incompatible validation or canonicalization.
func runProfileCheck(o opsCtx) error {
	stored, err := checkedProfile(o)
	if err != nil {
		return err
	}
	result, _ := assets.ValidateProfile(stored.ProfileRaw)
	if !result.Valid {
		fmt.Fprintf(o.out, "FAIL: stored profile %s does not validate with this build:\n", stored.ProjectID)
		for _, e := range result.Errors {
			fmt.Fprintf(o.out, "  - %s\n", e)
		}
		return fmt.Errorf("profile check: stored profile %s fails this build's validation; do not upgrade this deployment to it", stored.ProjectID)
	}
	if !strings.EqualFold(result.ProfileDigest, stored.Digest) {
		fmt.Fprintf(o.out, "FAIL: stored profile %s validates, but its digest is now %s instead of the stored %s\n", stored.ProjectID, result.ProfileDigest, stored.Digest)
		return fmt.Errorf("profile check: this build computes a different digest for stored profile %s; do not upgrade this deployment to it", stored.ProjectID)
	}
	fmt.Fprintf(o.out, "OK: stored profile %s validates with this build, digest %s unchanged\n", stored.ProjectID, stored.Digest)
	return nil
}

// checkedProfile returns the profile bound to the project record, the one record creation
// re-validates. Before deployment there is no project record, so it takes the latest stored
// profile, which the deployment will bind.
func checkedProfile(o opsCtx) (*models.AssetProfile, error) {
	p, err := o.repos.Projects.Get(o.ctx)
	switch {
	case err == nil:
		stored, err := o.repos.AssetProfiles.Get(o.ctx, p.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("profile check: loading the asset profile of project %s: %w", p.ProjectID, err)
		}
		return stored, nil
	case errors.Is(err, repository.ErrNotFound):
		stored, err := o.repos.AssetProfiles.GetCurrent(o.ctx)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, errors.New("profile check: no asset profile is stored, so there is nothing to check")
		}
		if err != nil {
			return nil, fmt.Errorf("profile check: loading the latest asset profile: %w", err)
		}
		return stored, nil
	default:
		return nil, fmt.Errorf("profile check: loading the project record: %w", err)
	}
}
