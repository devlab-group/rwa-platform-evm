// Package txindex builds Transaction records for indexed wallet actions the server's
// transaction manager did not submit, including purchases, redemptions, withdrawals and
// governance. GET /transactions shows one record per emitting txHash, with ID
// EventDerivedTxIDPrefix+txHash.
//
// Replays skip Removed events and reflect indexer rollbacks. SubmittedAt starts from
// the earliest event's IndexedAt, then stays pinned for stable replay and keyset
// pagination.
package txindex

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

// stackTxKinds ranks events for Kind, From, To and Value. Business/value-bearing events
// outrank RoleGranted/RoleRevoked; both price events map to "price_updated". Unlisted
// names, including generic topic0 strings, get lowest priority. Empty and literal
// "unknown" names are ignored; hashes without eligible events produce no record.
var stackTxKinds = []struct{ event, kind string }{
	{"ProceedsWithdrawn", "treasury_withdrawal"},
	{"Purchased", "purchase"},
	{"Minted", "mint"},
	{"Burned", "burn"},
	{"RedemptionRequested", "redemption_requested"},
	{"RedemptionFunded", "redemption_funded"},
	// claimRedemption emits RedemptionCompleted; there is no RedemptionClaimed event
	{"RedemptionCompleted", "redemption_claimed"},
	{"RedemptionRejected", "redemption_rejected"},
	{"RedemptionCancelled", "redemption_cancelled"},
	{"StatusChanged", "compliance_status_changed"},
	{"PurchasePriceUpdated", "price_updated"},
	{"RedemptionPriceUpdated", "price_updated"},
	{"AuditorChanged", "auditor_changed"},
	{"TreasuryChanged", "treasury_changed"},
	{"StrategyChanged", "strategy_changed"},
	{"Paused", "paused"},
	{"Unpaused", "unpaused"},
	{"DefaultAdminTransferScheduled", "admin_transfer_scheduled"},
	{"DefaultAdminTransferCanceled", "admin_transfer_canceled"},
	{"RoleGranted", "role_granted"},
	{"RoleRevoked", "role_revoked"},
}

var (
	kindByEvent = map[string]string{}
	rankByEvent = map[string]int{}
)

func init() {
	for i, k := range stackTxKinds {
		kindByEvent[k.event] = k.kind
		rankByEvent[k.event] = i
	}
}

// actorKeys orders actor fields; the first valid nonzero address wins. Events without
// one, such as Minted/Burned, leave From empty.
var actorKeys = []string{"caller", "sender", "funder", "buyer", "account", "beneficiary"}

// valueKeys orders amount fields for Value; none yields "0", as for role changes
var valueKeys = []string{"quoteAmount", "amount", "rwaAmount"}

// ReconcileStackTransactions replays chainID's events, skipping hashes owned by the
// transaction manager. Each remaining hash with a non-Removed, eligible event gets one
// confirmed record. Existing event-derived records without live eligible events become
// TxReorged and remain visible.
//
// Repeated calls and pre-deployment calls are safe. ReconciliationRequired returns nil
// without writes while the indexer needs recovery.
func ReconcileStackTransactions(ctx context.Context, txs repository.TransactionRepository, chainEvents repository.ChainEventRepository, checkpoints repository.IndexerCheckpointRepository, chainID int64) error {
	if cp, err := checkpoints.Get(ctx, chainID, indexer.CheckpointAddress); err == nil {
		if cp.ReconciliationRequired {
			return nil // indexer frozen mid-reorg
		}
	} else if err != repository.ErrNotFound {
		return fmt.Errorf("txindex: load indexer checkpoint: %w", err)
	}

	events, err := chainEvents.ListAll(ctx, chainID)
	if err != nil {
		return fmt.Errorf("txindex: list all chain events: %w", err)
	}
	byTx := map[string][]*models.ChainEvent{}
	for _, e := range events {
		byTx[e.TxHash] = append(byTx[e.TxHash], e)
	}

	now := time.Now().UTC()
	liveHashes := map[string]bool{}

	for txHash, evs := range byTx {
		var live []*models.ChainEvent
		for _, e := range evs {
			if !e.Removed {
				live = append(live, e)
			}
		}
		primary := pickPrimary(live)
		if primary == nil {
			continue // No non-Removed event has an eligible name
		}
		liveHashes[txHash] = true

		existing, err := txs.GetByTxHash(ctx, chainID, txHash)
		if err != nil && err != repository.ErrNotFound {
			return fmt.Errorf("txindex: get tx by hash %s: %w", txHash, err)
		}
		if existing != nil && !models.IsEventDerived(existing) {
			continue // a transaction-manager record already lists this tx
		}

		rec := buildEventDerivedTx(chainID, txHash, live, primary, existing, now)
		if existing == nil {
			if err := txs.Create(ctx, rec); err != nil {
				return fmt.Errorf("txindex: create event-derived tx %s: %w", txHash, err)
			}
		} else if err := txs.Update(ctx, rec); err != nil {
			return fmt.Errorf("txindex: update event-derived tx %s: %w", txHash, err)
		}
	}

	// Mark derived records TxReorged when Removed flags or rollback leave no live eligible
	// events; returning events are reconfirmed by the upsert loop
	all, err := txs.List(ctx)
	if err != nil {
		return fmt.Errorf("txindex: list transactions: %w", err)
	}
	for _, t := range all {
		if !models.IsEventDerived(t) || liveHashes[t.TxHash] || t.Status == models.TxReorged {
			continue
		}
		t.Status = models.TxReorged
		t.UpdatedAt = now
		if err := txs.Update(ctx, t); err != nil {
			return fmt.Errorf("txindex: mark reorged tx %s: %w", t.TxHash, err)
		}
	}
	return nil
}

// pickPrimary selects live's highest-priority eligible event, breaking ties by earliest
// (block, logIndex); nil means no eligible name
func pickPrimary(live []*models.ChainEvent) *models.ChainEvent {
	var best *models.ChainEvent
	bestRank := 0
	for _, e := range live {
		if e.Name == "" || e.Name == "unknown" {
			continue // Skip empty or literal "unknown" names
		}
		r, ok := rankByEvent[e.Name]
		if !ok {
			r = len(stackTxKinds) // decoded but unmapped: lowest priority
		}
		if best == nil || r < bestRank || (r == bestRank && earlier(e, best)) {
			best, bestRank = e, r
		}
	}
	return best
}

// buildEventDerivedTx takes Kind (mapped or raw name), From, To and Value from
// primary's name, actor, emitter and amount. Latest live event supplies
// BlockNumber/BlockHash; earliest supplies IndexedAt as SubmittedAt. An existing record
// contributes only pinned SubmittedAt.
func buildEventDerivedTx(chainID int64, txHash string, live []*models.ChainEvent, primary *models.ChainEvent, existing *models.Transaction, now time.Time) *models.Transaction {
	earliest, latest := live[0], live[0]
	for _, e := range live {
		if earlier(e, earliest) {
			earliest = e
		}
		if earlier(latest, e) {
			latest = e
		}
	}
	kind, ok := kindByEvent[primary.Name]
	if !ok {
		kind = primary.Name
	}
	submittedAt := earliest.IndexedAt
	if existing != nil && !existing.SubmittedAt.IsZero() {
		submittedAt = existing.SubmittedAt
	}
	return &models.Transaction{
		ID:          models.EventDerivedTxIDPrefix + txHash,
		ChainID:     chainID,
		TxHash:      txHash,
		Kind:        kind,
		From:        actorFrom(primary.Data),
		To:          primary.Address,
		Value:       valueFrom(primary.Data),
		Status:      models.TxConfirmed,
		BlockNumber: latest.BlockNumber,
		BlockHash:   latest.BlockHash,
		SubmittedAt: submittedAt,
		UpdatedAt:   now,
	}
}

// actorFrom returns the first valid, non-zero address among actorKeys in data,
// or "" when there is none
func actorFrom(data map[string]any) string {
	for _, k := range actorKeys {
		v, _ := data[k].(string)
		if v == "" || !common.IsHexAddress(v) {
			continue
		}
		if a := common.HexToAddress(v); a != (common.Address{}) {
			return a.Hex()
		}
	}
	return ""
}

// valueFrom returns the first non-empty amount among valueKeys in data, or "0"
func valueFrom(data map[string]any) string {
	for _, k := range valueKeys {
		if v, _ := data[k].(string); v != "" {
			return v
		}
	}
	return "0"
}

// earlier reports whether a precedes b in (block, logIndex) order
func earlier(a, b *models.ChainEvent) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	return a.LogIndex < b.LogIndex
}
