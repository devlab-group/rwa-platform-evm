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
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

// stackTxKinds ranks events for Kind, From, To and Value. Business/value-bearing events
// outrank RoleGranted/RoleRevoked; both price events map to "price_updated". Unlisted
// names, including generic topic0 strings, get lowest priority. Empty and literal
// "unknown" names are ignored, and ReconcileStackTransactions does not load the token's
// ERC-20 "Transfer" (indexed for income snapshots, not shown as history); hashes without
// eligible events produce no record.
var stackTxKinds = []struct{ event, kind string }{
	// Rank deployment above the role and status events emitted in the same transaction
	{"ProjectDeployed", "project_deployed"},
	{"ProceedsWithdrawn", "treasury_withdrawal"},
	{"Purchased", "purchase"},
	// settlePurchase that fills emits PurchaseSettled with Purchased, which ranks first;
	// one that refunds emits PurchaseRefunded alone
	{"PurchaseSettled", "purchase"},
	{"PurchaseRequested", "purchase_requested"},
	{"PurchaseRefunded", "purchase_refunded"},
	{"PurchaseCancelled", "purchase_cancelled"},
	{"PurchaseRejected", "purchase_rejected"},
	{"Minted", "mint"},
	{"Burned", "burn"},
	{"RedemptionRequested", "redemption_requested"},
	// an rwa-v4 oracle request also emits RedemptionForwardPriced
	{"RedemptionForwardPriced", "redemption_requested"},
	{"RedemptionFunded", "redemption_funded"},
	// funding an rwa-v4 oracle request emits RedemptionPriced before RedemptionFunded
	{"RedemptionPriced", "redemption_funded"},
	// claimRedemption emits RedemptionCompleted; there is no RedemptionClaimed event
	{"RedemptionCompleted", "redemption_claimed"},
	{"RedemptionRejected", "redemption_rejected"},
	{"RedemptionCancelled", "redemption_cancelled"},
	{"DistributionCreated", "income_distribution_created"},
	{"IncomeClaimed", "income_claimed"},
	{"UnclaimedReclaimed", "income_reclaimed"},
	{"ClaimDeadlineExtended", "income_deadline_extended"},
	{"MinPurchaseQuoteChanged", "min_purchase_changed"},
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
	{"DefaultAdminDelayChangeScheduled", "admin_delay_change_scheduled"},
	{"DefaultAdminDelayChangeCanceled", "admin_delay_change_canceled"},
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

// valueKeys orders amount fields for Value; none yields "0", as for role changes. refund
// precedes quoteAmount so a refunded order shows the deposit returned, not the quote it was
// not charged; maxQuoteAmount is the deposit of a purchase request.
var valueKeys = []string{"refund", "quoteAmount", "amount", "rwaAmount", "maxQuoteAmount", "total"}

// unnamedSignerEvents need a sender lookup: purchase settlement/rejection, redemption claims,
// freezes and seizures identify a buyer, beneficiary or holder rather than establishing the
// caller. That account may also be the signer, but cannot prove it. RedemptionRejected carries
// its caller and needs no lookup.
var unnamedSignerEvents = map[string]bool{
	"PurchaseSettled":     true,
	"PurchaseRefunded":    true,
	"PurchaseRejected":    true,
	"RedemptionCompleted": true,
	"Frozen":              true,
	"ForcedTransfer":      true,
}

// signerUnnamed identifies event groups that need a sender lookup. Purchased alongside
// settlement names the buyer; a standalone Purchased from buy names its caller.
func signerUnnamed(live []*models.ChainEvent) bool {
	for _, e := range live {
		if unnamedSignerEvents[e.Name] {
			return true
		}
	}
	return false
}

// SenderFunc returns the account that signed the transaction with the given hash
type SenderFunc func(ctx context.Context, txHash string) (string, error)

// SenderFromNode recovers a transaction's signer from the node
func SenderFromNode(client blockchain.Client, chainID int64) SenderFunc {
	signer := types.LatestSignerForChainID(big.NewInt(chainID))
	return func(ctx context.Context, txHash string) (string, error) {
		tx, _, err := client.TransactionByHash(ctx, common.HexToHash(txHash))
		if err != nil {
			return "", err
		}
		from, err := types.Sender(signer, tx)
		if err != nil {
			return "", err
		}
		return from.Hex(), nil
	}
}

// ReconcileStackTransactions replays chainID's events, skipping hashes owned by the
// transaction manager. Each remaining hash with a non-Removed, eligible event gets one
// confirmed record. Existing event-derived records without live eligible events become
// TxReorged and remain visible.
//
// Transactions identified by signerUnnamed get From from sender. A resolved value is retained
// on later passes; a missing or failed lookup leaves From empty and can be retried.
//
// Repeated calls and pre-deployment calls are safe. ReconciliationRequired returns nil
// without writes while the indexer needs recovery.
func ReconcileStackTransactions(ctx context.Context, txs repository.TransactionRepository, chainEvents repository.ChainEventRepository, checkpoints repository.IndexerCheckpointRepository, chainID int64, sender SenderFunc) error {
	if cp, err := checkpoints.Get(ctx, chainID, indexer.CheckpointAddress); err == nil {
		if cp.ReconciliationRequired {
			return nil // indexer frozen mid-reorg
		}
	} else if err != repository.ErrNotFound {
		return fmt.Errorf("txindex: load indexer checkpoint: %w", err)
	}

	// Exclude high-volume token Transfer logs used by income snapshots from transaction history
	events, err := chainEvents.ListAll(ctx, chainID, "Transfer")
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
		if rec.From == "" && sender != nil && signerUnnamed(live) {
			if from, err := sender(ctx, txHash); err != nil {
				log.Printf("txindex: sender of %s: %v", txHash, err)
			} else {
				rec.From = from
			}
		}
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

// buildEventDerivedTx takes Kind, To and Value from the primary event's name, emitter and
// amount. From uses its actor unless a sender lookup is required, in which case a previously
// resolved From is retained. The latest live event supplies BlockNumber/BlockHash; the earliest
// supplies SubmittedAt unless an existing record has already pinned it.
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
	from := actorFrom(primary.Data)
	if signerUnnamed(live) {
		from = ""
		if existing != nil {
			from = existing.From // looked up on an earlier pass
		}
	}
	return &models.Transaction{
		ID:          models.EventDerivedTxIDPrefix + txHash,
		ChainID:     chainID,
		TxHash:      txHash,
		Kind:        kind,
		From:        from,
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
