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

// ReconcileMinted projects SupplyController Minted events for (chainID, controllerAddr)
// by RecordKey, since events omit recordId. Matching records become Minted with the
// event's transaction hash, unless already Minted or Rejected.
// The indexer deletes reorg-orphaned events. A Minted record without a matching event
// returns to Signed with MintTxHash cleared. Rejected records remain unchanged;
// repeated reconciliation is safe.
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
			// Restore Signed after an orphaned mint
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

// ReconcileAuditor applies indexed SupplyController AuditorChanged events for (chainID,
// controllerAddr) in (blockNumber, logIndex) order, skipping Removed entries. Packages
// then use the latest auditor, including rotations made outside the server, avoiding
// WrongAuditor mint failures. Repeated calls are safe.
// If no event remains, including after reorg deletion, it restores NewRecordService's
// baseline auditor.
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

// ReconcileAuditorLive reads SupplyController.auditor() at startup without waiting for
// the indexer. ReconcileAuditor tracks subsequent indexed rotations.
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
