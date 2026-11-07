package sales

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// ErrOrdersNotConfigured is returned by the order reads when the service has no order repository
var ErrOrdersNotConfigured = errors.New("sales: purchase orders are not configured")

// ErrPurchaseTimeoutUnknown is returned by ReconcileOrders when Vault events exist but the
// purchase timeout is not positive; nothing is written
var ErrPurchaseTimeoutUnknown = errors.New("sales: purchase timeout unknown, orders not rebuilt")

// BuildOrders derives each models.PurchaseOrder from Vault events sorted by (blockNumber,
// logIndex). PurchaseRequested resets an order to Pending; the other events follow
// docs/spec/contracts.md:
//
// - Pending -> Settled or Refunded, both priced by a round
// - Pending -> Cancelled or Rejected, both returning the whole deposit
//
// Events without an id are ignored; events other than PurchaseRequested must match the
// current status. purchaseTimeout is Vault.purchaseTimeout in seconds.
func BuildOrders(events []*models.ChainEvent, purchaseTimeout int64) map[string]*models.PurchaseOrder {
	sorted := make([]*models.ChainEvent, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BlockNumber != sorted[j].BlockNumber {
			return sorted[i].BlockNumber < sorted[j].BlockNumber
		}
		return sorted[i].LogIndex < sorted[j].LogIndex
	})

	states := map[string]*models.PurchaseOrder{}
	now := time.Now().UTC()

	for _, e := range sorted {
		id := toString(e.Data["id"])
		if id == "" {
			continue
		}
		if e.Name == "PurchaseRequested" {
			createdAt := toInt64(e.Data["createdAt"])
			states[id] = &models.PurchaseOrder{
				ID:             id,
				Buyer:          toString(e.Data["buyer"]),
				Recipient:      toString(e.Data["recipient"]),
				TokenAmount:    toString(e.Data["tokenAmount"]),
				MaxQuoteAmount: toString(e.Data["maxQuoteAmount"]),
				Strategy:       toString(e.Data["strategy"]),
				BaseRound:      toString(e.Data["baseRound"]),
				Status:         models.PurchaseOrderPending,
				CreatedAt:      createdAt,
				TimeoutAt:      createdAt + purchaseTimeout,
				RequestTxHash:  e.TxHash,
				UpdatedAt:      now,
			}
			continue
		}
		o, ok := states[id]
		if !ok || o.Status != models.PurchaseOrderPending {
			continue
		}
		switch e.Name {
		case "PurchaseSettled", "PurchaseRefunded":
			o.Status = models.PurchaseOrderSettled
			if e.Name == "PurchaseRefunded" {
				o.Status = models.PurchaseOrderRefunded
			}
			o.PricedRound = toString(e.Data["roundId"])
			o.QuoteAmount = toString(e.Data["quoteAmount"])
			o.Refund = toString(e.Data["refund"])
		case "PurchaseCancelled", "PurchaseRejected":
			o.Status = models.PurchaseOrderCancelled
			if e.Name == "PurchaseRejected" {
				o.Status = models.PurchaseOrderRejected
			}
			o.Refund = toString(e.Data["refund"])
		default:
			continue
		}
		o.CloseTxHash = e.TxHash
		o.UpdatedAt = now
	}
	return states
}

// toInt64 handles ChainEvent.Data integers from the indexer, MongoDB int32/int64, JSON
// float64 and decimal strings
func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case uint64:
		return int64(t)
	case int32:
		return int64(t)
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

// WithOrders attaches the order read model, which ReconcileOrders, ListOrdersPage and
// GetOrder need, and returns s
func (s *Service) WithOrders(orders repository.PurchaseOrderRepository) *Service {
	s.orders = orders
	return s
}

// ReconcileOrders rebuilds purchase_orders from persisted Vault events for (chainID, vaultAddr),
// including indexer rollbacks. It writes surviving orders with a new generation before pruning
// stale rows, keeping those IDs readable throughout the rebuild.
func (s *Service) ReconcileOrders(ctx context.Context, chainEvents repository.ChainEventRepository, chainID int64, purchaseTimeout int64) error {
	if s.orders == nil {
		return ErrOrdersNotConfigured
	}
	var all []*models.ChainEvent
	for _, name := range OrderEventNames {
		evs, err := chainEvents.ListByName(ctx, chainID, s.vaultAddr.Hex(), name)
		if err != nil {
			return fmt.Errorf("sales: list %s events: %w", name, err)
		}
		all = append(all, evs...)
	}

	// every order's timeoutAt is createdAt plus the timeout, so rebuilding with none would
	// publish each open order as already cancellable
	if purchaseTimeout <= 0 && len(all) > 0 {
		return ErrPurchaseTimeoutUnknown
	}

	// Every order is rewritten each tick. Skipping unchanged rows would require pruning
	// without the generation stamp, which identifies rows retained by this rebuild.
	gen := time.Now().UnixNano()
	for _, o := range BuildOrders(all, purchaseTimeout) {
		o.Generation = gen
		if err := s.orders.Upsert(ctx, o); err != nil {
			return fmt.Errorf("sales: upsert order %s: %w", o.ID, err)
		}
	}
	if err := s.orders.DeleteStaleGeneration(ctx, gen); err != nil {
		return fmt.Errorf("sales: prune stale orders: %w", err)
	}
	return nil
}

// ListOrdersPage uses keyset pagination by newest CreatedAt, with optional status and
// case-insensitive buyer address filters; see repository.PurchaseOrderRepository.ListPage
func (s *Service) ListOrdersPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.PurchaseOrder, string, error) {
	if s.orders == nil {
		return nil, "", ErrOrdersNotConfigured
	}
	return s.orders.ListPage(ctx, status, address, cursor, limit)
}

// PendingOrders returns every Pending purchase order, newest first
func (s *Service) PendingOrders(ctx context.Context) ([]*models.PurchaseOrder, error) {
	if s.orders == nil {
		return nil, nil // no order read model: nothing is pending to report
	}
	var out []*models.PurchaseOrder
	cursor := ""
	for {
		page, next, err := s.ListOrdersPage(ctx, string(models.PurchaseOrderPending), "", cursor, 500)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// GetOrder returns one purchase order by ID
func (s *Service) GetOrder(ctx context.Context, id string) (*models.PurchaseOrder, error) {
	if s.orders == nil {
		return nil, ErrOrdersNotConfigured
	}
	return s.orders.Get(ctx, id)
}
