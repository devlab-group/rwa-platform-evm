package assets

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// ReconcileMinted scans indexed SupplyController Minted events for
// (chainID, controllerAddr) and flips any matching Pending/Signed
// AssetRecord to Minted. Anyone may submit the valid signature to
// SupplyController.mint, and tokens are minted only to the Vault. Matching is
// by RecordKey since Minted carries recordKey, not recordId, and
// record.RecordKey was computed and stored at CreateRecord time. Safe to call
// repeatedly: already-Minted records backed by a surviving event are left
// alone.
//
// The projection is reversible: a record promoted to Minted by a prior call
// is DEMOTED back to Signed if its backing Minted event is no longer
// present — e.g. an indexer reorg rollback (internal/indexer) discarded it
// because it was orphaned. chainEvents only ever holds events the indexer
// currently considers canonical, so "no surviving event for this
// RecordKey" is exactly the condition that must be reflected here; this
// reconciler must not be append-only or a rolled-back mint would leave a
// record permanently (and incorrectly) stuck at Minted.
func (s *RecordService) ReconcileMinted(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, controllerAddr string, records repository.AssetRecordRepository) error {
	events, err := chainEvents.ListByName(ctx, chainID, controllerAddr, "Minted")
	if err != nil {
		return fmt.Errorf("assets: list Minted events: %w", err)
	}

	byRecordKey := map[string]*models.ChainEvent{}
	for _, e := range events {
		if rk, _ := e.Data["recordKey"].(string); rk != "" {
			byRecordKey[rk] = e
		}
	}

	all, err := records.List(ctx)
	if err != nil {
		return fmt.Errorf("assets: list records: %w", err)
	}
	now := time.Now().UTC()
	for _, rec := range all {
		event, backed := byRecordKey[rec.RecordKey]
		switch {
		case rec.Status == models.RecordStatusMinted && !backed:
			// The Minted event that produced this record's mint was
			// orphaned (rolled back). Demote to Signed — the state
			// immediately before minting — so the still-valid signed
			// package can mint again once a genuine Minted event exists.
			// Never touch Rejected: it's a terminal, non-chain-derived
			// status this reconciler never set in the first place.
			rec.Status = models.RecordStatusSigned
			rec.MintTxHash = ""
			rec.UpdatedAt = now
			if err := records.Update(ctx, rec); err != nil {
				return fmt.Errorf("assets: demote record %s from orphaned Minted: %w", rec.RecordID, err)
			}
		case rec.Status != models.RecordStatusMinted && rec.Status != models.RecordStatusRejected && backed:
			rec.Status = models.RecordStatusMinted
			rec.MintTxHash = event.TxHash
			rec.UpdatedAt = now
			if err := records.Update(ctx, rec); err != nil {
				return fmt.Errorf("assets: update record %s to Minted: %w", rec.RecordID, err)
			}
		}
	}
	return nil
}

// ReconcileAuditor projects indexed SupplyController AuditorChanged events
// for (chainID, controllerAddr) onto RecordService's live auditor address.
// A rotation performed on-chain via SupplyController.setAuditor — outside
// this server, e.g. by the admin/
// multisig — otherwise never reaches a running process: BuildPackage and
// RelaySignedResult would keep attesting/verifying against whatever
// auditor was known at construction (or activation) time, producing
// packages/verifications the contract's mint() now rejects (WrongAuditor).
// Safe to call repeatedly: it only ever applies the most recent event
// (by (blockNumber,logIndex), the order ListByName returns) and setting
// the same address again is a no-op.
//
// If NO surviving AuditorChanged event exists — the rotation that set the
// currently-live auditor was itself rolled back by a reorg
// (indexer.Indexer.rollback deletes the underlying chain_events row
// entirely, it does not merely flag it Removed) — this restores
// baselineAuditor (the deployment-time value RecordService was
// constructed with) rather than leaving the live auditor stuck at an
// address with no on-chain record justifying it anymore. Previously this
// returned nil without restoring anything, so the service kept
// creating/verifying packages against the orphaned auditor until restart
// or another rotation.
func (s *RecordService) ReconcileAuditor(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, controllerAddr string) error {
	events, err := chainEvents.ListByName(ctx, chainID, controllerAddr, "AuditorChanged")
	if err != nil {
		return fmt.Errorf("assets: list AuditorChanged events: %w", err)
	}
	var latest *models.ChainEvent
	for _, e := range events {
		if e.Removed {
			continue // a reorged-out log is not a real rotation
		}
		latest = e
	}
	if latest == nil {
		s.SetAuditor(s.baselineAuditor)
		return nil
	}
	newAuditor, _ := latest.Data["newAuditor"].(string)
	if newAuditor == "" || !common.IsHexAddress(newAuditor) {
		return fmt.Errorf("assets: AuditorChanged event %s/%d has no valid newAuditor", latest.TxHash, latest.LogIndex)
	}
	s.SetAuditor(common.HexToAddress(newAuditor))
	return nil
}

// ReconcileAuditorLive reads SupplyController.auditor() directly from the
// chain and applies it. Unlike ReconcileAuditor this doesn't
// depend on the indexer having caught up, so it's the right check to run
// once, synchronously, right after activation/boot — ReconcileAuditor's
// ticker then keeps it current for any rotation afterward without a fresh
// RPC round-trip on every tick.
func (s *RecordService) ReconcileAuditorLive(ctx context.Context, client blockchain.Client) error {
	data, err := s.controller.ABI.Pack("auditor")
	if err != nil {
		return err
	}
	out, err := blockchain.Call(ctx, client, s.controllerAddr, data)
	if err != nil {
		return fmt.Errorf("assets: read SupplyController.auditor(): %w", err)
	}
	unpacked, err := s.controller.ABI.Unpack("auditor", out)
	if err != nil {
		return err
	}
	s.SetAuditor(*abi.ConvertType(unpacked[0], new(common.Address)).(*common.Address))
	return nil
}
