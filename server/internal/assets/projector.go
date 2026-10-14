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

// ReconcileMinted projects the indexed SupplyController Minted events for
// (chainID, controllerAddr) onto asset records. A record that is neither Minted
// nor Rejected and has a matching event becomes Minted and takes the event's
// transaction hash. Events are matched by RecordKey because Minted carries
// recordKey, not recordId, and CreateRecord stores RecordKey.
//
// The projection is reversible. The indexer deletes events it rolls back in a
// reorg, so chainEvents holds only events it currently considers canonical. A
// Minted record with no matching event goes back to Signed and its MintTxHash is
// cleared. Rejected records are never changed. Repeated calls are safe.
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
			// the Minted event was rolled back; Signed is the state before minting,
			// so the signed package can mint again
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

// ReconcileAuditor sets the live auditor address from the most recent indexed
// SupplyController AuditorChanged event for (chainID, controllerAddr). The
// contract admin can call SupplyController.setAuditor outside this server, and
// without this the process would keep building packages for the old auditor,
// which mint rejects with WrongAuditor. Events that ListByName returns in
// (blockNumber, logIndex) order are applied in that order, events marked Removed
// are skipped, and repeated calls are safe.
//
// When no AuditorChanged event remains, because the indexer deleted the
// rotation's event in a reorg rollback, it restores the baseline auditor given to
// NewRecordService.
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

// ReconcileAuditorLive reads SupplyController.auditor() from the chain and sets
// it as the live auditor. It does not depend on the indexer having caught up, so
// the platform calls it once at startup, and ReconcileAuditor follows later
// rotations from indexed events.
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
