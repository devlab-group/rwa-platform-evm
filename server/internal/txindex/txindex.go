// Package txindex projects indexed chain events into Transaction records for
// on-chain transactions the server did NOT itself submit. Now that
// admin/treasurer/investor actions (treasury withdrawals, role changes,
// pauses, price updates, buys, redemption lifecycle, admin transfers,
// compliance status changes) are broadcast from connected wallets rather than
// the server tx-manager, those transactions never get a tx-manager Transaction
// record, so GET /transactions (and the investor/admin tx lists) would miss
// them. ReconcileStackTransactions closes that gap by synthesizing one
// event-derived Transaction per emitting txHash.
//
// Like project.ReconcileSecurity and the other read-model reconcilers, it is a
// FULL REPLAY over whatever chain_events currently holds — not an append-only
// delta feed — which is what makes it reorg-safe: the indexer soft-marks
// reorged rows Removed and deletes them on deep rollback, and re-deriving from
// scratch every tick reflects both an out-of-band action AND its later reorg.
// It is idempotent: each record's ID is a deterministic function of the txHash
// (EventDerivedTxIDPrefix+txHash) and its SubmittedAt is pinned to the earliest
// event's IndexedAt (preserved once written), so repeated runs and stable
// keyset pagination both hold.
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

// stackTxKinds maps each decoded stack event to a human-readable Transaction
// Kind, in PRIMARY-EVENT PRIORITY ORDER (earliest = highest priority). A single
// transaction can emit several events (a buy emits Purchased plus ERC20
// Transfers; accepting an admin transfer emits RoleRevoked+RoleGranted); the
// record's Kind/From/To/Value come from the highest-priority decoded event in
// the tx. Business/value-bearing actions rank above pure authority bookkeeping
// (RoleGranted/RoleRevoked rank last) so, e.g., a buy reads as "purchase" not a
// token Transfer, and an admin accept reads by its role move. Both price events
// collapse to "price_updated". A decoded event absent from this list still
// yields a record with Kind = its raw event name (lowest priority); only
// undecodable logs (Name "unknown", e.g. raw ERC20 Transfers) are ignored — a
// txHash with no decoded stack event produces no record.
var stackTxKinds = []struct{ event, kind string }{
	{"ProceedsWithdrawn", "treasury_withdrawal"},
	{"Purchased", "purchase"},
	{"Minted", "mint"},
	{"Burned", "burn"},
	{"RedemptionRequested", "redemption_requested"},
	{"RedemptionFunded", "redemption_funded"},
	// claimRedemption emits RedemptionCompleted (there is no distinct
	// "RedemptionClaimed" event) — it is the claim, so it maps to that kind.
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

// actorKeys are the event-data fields, in priority order, that name the acting
// EOA behind a transaction. The first present, valid, non-zero address wins;
// events that carry none (e.g. Minted/Burned, whose relayer isn't in the log)
// leave From empty.
var actorKeys = []string{"caller", "sender", "funder", "buyer", "account", "beneficiary"}

// valueKeys are the event-data amount fields, in priority order, used for the
// record's Value (quote-token minimal units where applicable). "0" when none
// apply (e.g. a role change carries no amount).
var valueKeys = []string{"quoteAmount", "amount", "rwaAmount"}

// ReconcileStackTransactions replays every chain event for chainID into
// event-derived Transaction records — see the package doc comment. It:
//   - skips a txHash a tx-manager record already owns (dedup, so a
//     server-relayed tx such as a mint isn't double-listed);
//   - upserts one record per remaining txHash that has ≥1 non-removed decoded
//     stack event, Status confirmed;
//   - flips any previously-written event-derived record whose txHash is no
//     longer live (all events soft-removed or deleted on deep rollback) to
//     TxReorged (updated, not deleted, so the UI keeps a distinct reorged
//     state).
//
// Safe to call repeatedly and before any deployment. Like the other derived
// read models it freezes while the indexer is mid-reorg
// (checkpoint.ReconciliationRequired), returning nil rather than projecting a
// known-inconsistent view.
func ReconcileStackTransactions(ctx context.Context, txs repository.TransactionRepository, chainEvents repository.ChainEventRepository, checkpoints repository.IndexerCheckpointRepository, chainID int64) error {
	if cp, err := checkpoints.Get(ctx, chainID, indexer.CheckpointAddress); err == nil {
		if cp.ReconciliationRequired {
			return nil // indexer frozen mid-reorg — do not project a stale view
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
			continue // no non-removed decoded stack event → nothing to track
		}
		liveHashes[txHash] = true

		existing, err := txs.GetByTxHash(ctx, chainID, txHash)
		if err != nil && err != repository.ErrNotFound {
			return fmt.Errorf("txindex: get tx by hash %s: %w", txHash, err)
		}
		if existing != nil && !models.IsEventDerived(existing) {
			continue // a tx-manager record already lists this tx — don't duplicate
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

	// Reorg pass: an event-derived record whose txHash is no longer live had
	// all its events soft-removed or deleted out from under it — mark it
	// reorged (a resurrected txHash is re-confirmed by the upsert loop above).
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

// pickPrimary returns the highest-priority decoded event among the tx's
// non-removed events, or nil when none is a decoded stack event (only
// undecodable "unknown" logs). Ties on priority break by (block, logIndex) for
// determinism.
func pickPrimary(live []*models.ChainEvent) *models.ChainEvent {
	var best *models.ChainEvent
	bestRank := 0
	for _, e := range live {
		if e.Name == "" || e.Name == "unknown" {
			continue // undecodable log (e.g. a raw ERC20 Transfer)
		}
		r, ok := rankByEvent[e.Name]
		if !ok {
			r = len(stackTxKinds) // decoded but unmapped: lowest priority, still eligible
		}
		if best == nil || r < bestRank || (r == bestRank && earlier(e, best)) {
			best, bestRank = e, r
		}
	}
	return best
}

// buildEventDerivedTx assembles the event-derived Transaction from the tx's
// non-removed events and its chosen primary event. To is the primary event's
// emitting contract; From the primary's acting EOA; Value its amount; Kind the
// mapped (or raw) event name. BlockNumber/BlockHash come from the latest event
// and SubmittedAt from the earliest (preserved once first written), both stable
// under replay. existing, when an event-derived record, contributes only its
// pinned SubmittedAt.
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

// actorFrom returns the first valid, non-zero acting-EOA address among
// actorKeys in data, or "" when none is present.
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

// valueFrom returns the first present amount among valueKeys in data, or "0".
func valueFrom(data map[string]any) string {
	for _, k := range valueKeys {
		if v, _ := data[k].(string); v != "" {
			return v
		}
	}
	return "0"
}

// earlier reports whether a precedes b in (block, logIndex) order.
func earlier(a, b *models.ChainEvent) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	return a.LogIndex < b.LogIndex
}
