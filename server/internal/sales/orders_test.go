package sales

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

const purchaseTimeout = 604800

func orderEv(name, id string, block uint64, logIndex uint, extra map[string]any) *models.ChainEvent {
	data := map[string]any{"id": id}
	for k, v := range extra {
		data[k] = v
	}
	return &models.ChainEvent{Name: name, BlockNumber: block, LogIndex: logIndex, TxHash: name + "-" + id, Data: data}
}

func requested(id string, block uint64, createdAt uint64) *models.ChainEvent {
	return orderEv("PurchaseRequested", id, block, 0, map[string]any{
		"buyer":          "0xB1",
		"recipient":      "0xC2",
		"tokenAmount":    "1000",
		"maxQuoteAmount": "2500",
		"strategy":       "0x57",
		"baseRound":      "36893488147419103232",
		"createdAt":      createdAt,
	})
}

func TestBuildOrdersRequestedIsPendingWithTimeout(t *testing.T) {
	got := BuildOrders([]*models.ChainEvent{requested("3", 10, 1_700_000_000)}, purchaseTimeout)["3"]
	if got == nil {
		t.Fatal("no order for id 3")
	}
	want := models.PurchaseOrder{
		ID:             "3",
		Buyer:          "0xB1",
		Recipient:      "0xC2",
		TokenAmount:    "1000",
		MaxQuoteAmount: "2500",
		Strategy:       "0x57",
		BaseRound:      "36893488147419103232",
		Status:         models.PurchaseOrderPending,
		CreatedAt:      1_700_000_000,
		TimeoutAt:      1_700_000_000 + 604800,
		RequestTxHash:  "PurchaseRequested-3",
	}
	got.UpdatedAt = time.Time{}
	if *got != want {
		t.Errorf("order = %+v, want %+v", *got, want)
	}
}

func TestBuildOrdersClosingEvents(t *testing.T) {
	tests := []struct {
		name  string
		close *models.ChainEvent
		want  models.PurchaseOrder
	}{
		{
			name:  "settled carries the pricing round, quote and unused deposit",
			close: orderEv("PurchaseSettled", "1", 12, 0, map[string]any{"roundId": "36893488147419103233", "quoteAmount": "2400", "refund": "100"}),
			want:  models.PurchaseOrder{Status: models.PurchaseOrderSettled, PricedRound: "36893488147419103233", QuoteAmount: "2400", Refund: "100"},
		},
		{
			name:  "refunded carries the round and the quote it was not charged",
			close: orderEv("PurchaseRefunded", "1", 12, 0, map[string]any{"roundId": "36893488147419103233", "quoteAmount": "2600", "refund": "2500"}),
			want:  models.PurchaseOrder{Status: models.PurchaseOrderRefunded, PricedRound: "36893488147419103233", QuoteAmount: "2600", Refund: "2500"},
		},
		{
			name:  "cancelled returns the whole deposit and has no pricing round",
			close: orderEv("PurchaseCancelled", "1", 12, 0, map[string]any{"buyer": "0xB1", "refund": "2500"}),
			want:  models.PurchaseOrder{Status: models.PurchaseOrderCancelled, Refund: "2500"},
		},
		{
			name:  "rejected returns the whole deposit and has no pricing round",
			close: orderEv("PurchaseRejected", "1", 12, 0, map[string]any{"buyer": "0xB1", "refund": "2500"}),
			want:  models.PurchaseOrder{Status: models.PurchaseOrderRejected, Refund: "2500"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildOrders([]*models.ChainEvent{requested("1", 10, 1000), tt.close}, purchaseTimeout)["1"]
			if got == nil {
				t.Fatal("no order for id 1")
			}
			if got.Status != tt.want.Status || got.PricedRound != tt.want.PricedRound || got.QuoteAmount != tt.want.QuoteAmount || got.Refund != tt.want.Refund {
				t.Errorf("order = status %s round %q quote %q refund %q, want status %s round %q quote %q refund %q",
					got.Status, got.PricedRound, got.QuoteAmount, got.Refund, tt.want.Status, tt.want.PricedRound, tt.want.QuoteAmount, tt.want.Refund)
			}
			if got.CloseTxHash != tt.close.TxHash || got.RequestTxHash != "PurchaseRequested-1" {
				t.Errorf("tx hashes = request %q close %q, want the request and the closing event's", got.RequestTxHash, got.CloseTxHash)
			}
		})
	}
}

func TestBuildOrdersIgnoresIllegalTransitions(t *testing.T) {
	settle := orderEv("PurchaseSettled", "1", 12, 0, map[string]any{"roundId": "7", "quoteAmount": "2400", "refund": "100"})
	tests := []struct {
		name   string
		events []*models.ChainEvent
		want   models.PurchaseOrderStatus
		quote  string
	}{
		{
			"cancel after settle keeps Settled",
			[]*models.ChainEvent{
				requested("1", 10, 1000),
				settle,
				orderEv("PurchaseCancelled", "1", 13, 0, map[string]any{"refund": "2500"}),
			},
			models.PurchaseOrderSettled,
			"2400",
		},
		{
			"second settle keeps the first",
			[]*models.ChainEvent{
				requested("1", 10, 1000),
				settle,
				orderEv("PurchaseSettled", "1", 13, 0, map[string]any{"roundId": "8", "quoteAmount": "1", "refund": "0"}),
			},
			models.PurchaseOrderSettled,
			"2400",
		},
		{
			"settle after reject keeps Rejected",
			[]*models.ChainEvent{
				requested("1", 10, 1000),
				orderEv("PurchaseRejected", "1", 11, 0, map[string]any{"refund": "2500"}),
				settle,
			},
			models.PurchaseOrderRejected,
			"",
		},
		{
			"refunded after cancel keeps Cancelled",
			[]*models.ChainEvent{
				requested("1", 10, 1000),
				orderEv("PurchaseCancelled", "1", 11, 0, map[string]any{"refund": "2500"}),
				orderEv("PurchaseRefunded", "1", 12, 0, map[string]any{"roundId": "7", "quoteAmount": "9", "refund": "9"}),
			},
			models.PurchaseOrderCancelled,
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildOrders(tt.events, purchaseTimeout)["1"]
			if got.Status != tt.want || got.QuoteAmount != tt.quote {
				t.Errorf("order = status %s quote %q, want %s %q", got.Status, got.QuoteAmount, tt.want, tt.quote)
			}
		})
	}

	t.Run("closing events for an order never requested create nothing", func(t *testing.T) {
		states := BuildOrders([]*models.ChainEvent{settle, orderEv("PurchaseCancelled", "9", 3, 0, nil)}, purchaseTimeout)
		if len(states) != 0 {
			t.Errorf("got %d orders from closing events alone, want 0", len(states))
		}
	})
	t.Run("an unrelated event for a pending order leaves it pending", func(t *testing.T) {
		got := BuildOrders([]*models.ChainEvent{
			requested("1", 10, 1000),
			orderEv("Purchased", "1", 11, 0, map[string]any{"quoteAmount": "1"}),
			orderEv("StrategyChanged", "1", 12, 0, nil),
		}, purchaseTimeout)["1"]
		if got.Status != models.PurchaseOrderPending || got.CloseTxHash != "" || got.QuoteAmount != "" {
			t.Errorf("order = %+v, want it untouched by events that do not close an order", got)
		}
	})
	t.Run("events without an id and unrelated events are ignored", func(t *testing.T) {
		states := BuildOrders([]*models.ChainEvent{
			{Name: "PurchaseRequested", BlockNumber: 1, Data: map[string]any{"buyer": "0xB1"}},
			orderEv("Purchased", "1", 2, 0, nil),
			orderEv("StrategyChanged", "1", 3, 0, nil),
		}, purchaseTimeout)
		if len(states) != 0 {
			t.Errorf("got %d orders, want 0", len(states))
		}
	})
}

func TestBuildOrdersReplaysInBlockAndLogOrderNotInputOrder(t *testing.T) {
	// the cancel logs before the settle in the same block, so the order ends Cancelled whatever
	// order the repository hands the events over in
	events := []*models.ChainEvent{
		orderEv("PurchaseSettled", "1", 12, 5, map[string]any{"roundId": "7", "quoteAmount": "2400", "refund": "100"}),
		orderEv("PurchaseCancelled", "1", 12, 2, map[string]any{"refund": "2500"}),
		requested("1", 10, 1000),
	}
	got := BuildOrders(events, purchaseTimeout)["1"]
	if got.Status != models.PurchaseOrderCancelled {
		t.Errorf("Status = %s, want Cancelled (cancel is first by log index)", got.Status)
	}
	if events[0].Name != "PurchaseSettled" {
		t.Error("BuildOrders reordered the caller's slice")
	}
}

func TestBuildOrdersRequestedResetsAnOrderToPending(t *testing.T) {
	// an id re-requested after a reorg replays from scratch
	events := []*models.ChainEvent{
		requested("1", 10, 1000),
		orderEv("PurchaseRejected", "1", 11, 0, map[string]any{"refund": "2500"}),
		requested("1", 20, 2000),
	}
	got := BuildOrders(events, purchaseTimeout)["1"]
	if got.Status != models.PurchaseOrderPending || got.CreatedAt != 2000 || got.Refund != "" || got.CloseTxHash != "" {
		t.Errorf("order = %+v, want a fresh Pending order created at 2000", got)
	}
}

func TestBuildOrdersReadsCreatedAtFromEveryStoredNumericForm(t *testing.T) {
	for name, v := range map[string]any{"uint64": uint64(1000), "int32": int32(1000), "int64": int64(1000), "float64": float64(1000), "string": "1000"} {
		t.Run(name, func(t *testing.T) {
			ev := requested("1", 10, 0)
			ev.Data["createdAt"] = v
			got := BuildOrders([]*models.ChainEvent{ev}, 60)["1"]
			if got.CreatedAt != 1000 || got.TimeoutAt != 1060 {
				t.Errorf("createdAt/timeoutAt = %d/%d, want 1000/1060", got.CreatedAt, got.TimeoutAt)
			}
		})
	}
}

// probedOrderRepo reads between generation writes to detect empty or partial rebuild views
type probedOrderRepo struct {
	repository.PurchaseOrderRepository
	probe func()
}

func (d probedOrderRepo) Upsert(ctx context.Context, o *models.PurchaseOrder) error {
	d.probe()
	return d.PurchaseOrderRepository.Upsert(ctx, o)
}

func (d probedOrderRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	d.probe()
	return d.PurchaseOrderRepository.DeleteStaleGeneration(ctx, gen)
}

func seedEvent(t *testing.T, events *memory.ChainEventRepository, vault common.Address, e *models.ChainEvent) {
	t.Helper()
	e.ChainID, e.Address = 31337, vault.Hex()
	if err := events.Create(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOrdersRebuildsAndFollowsAReorg(t *testing.T) {
	vault := common.HexToAddress("0x0000000000000000000000000000000000000A")
	events := memory.NewChainEventRepository()
	orders := memory.NewPurchaseOrderRepository()
	svc := New(nil, vault, common.Address{}, memory.NewPurchaseRepository()).WithOrders(orders)
	ctx := context.Background()

	seedEvent(t, events, vault, requested("1", 10, 1000))
	seedEvent(t, events, vault, requested("2", 11, 1100))
	seedEvent(t, events, vault, orderEv("PurchaseSettled", "1", 12, 0, map[string]any{"roundId": "7", "quoteAmount": "2400", "refund": "100"}))
	if err := svc.ReconcileOrders(ctx, events, 31337, purchaseTimeout); err != nil {
		t.Fatal(err)
	}
	if o, err := svc.GetOrder(ctx, "1"); err != nil || o.Status != models.PurchaseOrderSettled || o.PricedRound != "7" {
		t.Fatalf("order 1 = %+v, %v; want Settled at round 7", o, err)
	}
	if o, err := svc.GetOrder(ctx, "2"); err != nil || o.Status != models.PurchaseOrderPending || o.TimeoutAt != 1100+purchaseTimeout {
		t.Fatalf("order 2 = %+v, %v; want Pending", o, err)
	}

	// a reorg drops block 12 onwards: the settle is gone, so order 1 is Pending again
	if _, err := events.DeleteFromBlock(ctx, 31337, vault.Hex(), 12); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileOrders(ctx, events, 31337, purchaseTimeout); err != nil {
		t.Fatal(err)
	}
	if o, _ := svc.GetOrder(ctx, "1"); o == nil || o.Status != models.PurchaseOrderPending || o.PricedRound != "" || o.CloseTxHash != "" {
		t.Errorf("order 1 after the reorg = %+v, want Pending with no pricing round", o)
	}

	// a deeper reorg drops the requests themselves: the orders disappear
	if _, err := events.DeleteFromBlock(ctx, 31337, vault.Hex(), 11); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileOrders(ctx, events, 31337, purchaseTimeout); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetOrder(ctx, "2"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("order 2 after a reorg past its request: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetOrder(ctx, "1"); err != nil {
		t.Errorf("order 1 (block 10) was dropped by a reorg from block 11: %v", err)
	}
}

func TestReconcileOrdersOnlyReadsVaultOrderEvents(t *testing.T) {
	vault := common.HexToAddress("0x0000000000000000000000000000000000000A")
	other := common.HexToAddress("0x0000000000000000000000000000000000000B")
	events := memory.NewChainEventRepository()
	orders := memory.NewPurchaseOrderRepository()
	svc := New(nil, vault, common.Address{}, memory.NewPurchaseRepository()).WithOrders(orders)
	ctx := context.Background()

	// the same event on another contract, or on another chain, is not this Vault's order
	seedEvent(t, events, other, requested("1", 10, 1000))
	foreign := requested("2", 10, 1000)
	foreign.TxHash += "-foreign"
	foreign.ChainID, foreign.Address = 1, vault.Hex()
	if err := events.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileOrders(ctx, events, 31337, purchaseTimeout); err != nil {
		t.Fatal(err)
	}
	if page, _, err := svc.ListOrdersPage(ctx, "", "", "", 50); err != nil || len(page) != 0 {
		t.Errorf("orders = %v (err %v), want none", page, err)
	}
}

// Replace the seeded orders while probing; the count must never fall below the pre-rebuild
// value, which would mean a delete-first gap
func TestReconcileOrdersNeverExposesEmptyOrPartialOrdersBetweenWrites(t *testing.T) {
	vault := common.HexToAddress("0x0000000000000000000000000000000000000A")
	events := memory.NewChainEventRepository()
	inner := memory.NewPurchaseOrderRepository()
	armed, probes, minObserved := false, 0, 1<<30
	probed := probedOrderRepo{
		PurchaseOrderRepository: inner,
		probe: func() {
			if !armed {
				return
			}
			probes++
			page, _, err := inner.ListPage(context.Background(), "", "", "", 200)
			if err != nil {
				t.Errorf("probe ListPage: %v", err)
				return
			}
			minObserved = min(minObserved, len(page))
		},
	}
	svc := New(nil, vault, common.Address{}, memory.NewPurchaseRepository()).WithOrders(probed)
	ctx := context.Background()

	for i, id := range []string{"1", "2", "3"} {
		seedEvent(t, events, vault, requested(id, uint64(10+i), 1000))
	}
	if err := svc.ReconcileOrders(ctx, events, 31337, purchaseTimeout); err != nil {
		t.Fatal(err)
	}
	// every order survives with a new state: the rebuild rewrites them in place
	for i, id := range []string{"1", "2", "3"} {
		seedEvent(t, events, vault, orderEv("PurchaseCancelled", id, uint64(20+i), 0, map[string]any{"refund": "2500"}))
	}
	armed = true
	if err := svc.ReconcileOrders(ctx, events, 31337, purchaseTimeout); err != nil {
		t.Fatal(err)
	}
	if probes == 0 {
		t.Fatal("the probe never ran")
	}
	if minObserved != 3 {
		t.Errorf("readers saw as few as %d orders mid-rebuild, want 3 throughout", minObserved)
	}
}

func TestOrderReadsNeedTheOrderRepository(t *testing.T) {
	svc := New(nil, common.Address{}, common.Address{}, memory.NewPurchaseRepository())
	ctx := context.Background()
	if err := svc.ReconcileOrders(ctx, memory.NewChainEventRepository(), 31337, 1); !errors.Is(err, ErrOrdersNotConfigured) {
		t.Errorf("ReconcileOrders err = %v, want ErrOrdersNotConfigured", err)
	}
	if _, _, err := svc.ListOrdersPage(ctx, "", "", "", 10); !errors.Is(err, ErrOrdersNotConfigured) {
		t.Errorf("ListOrdersPage err = %v, want ErrOrdersNotConfigured", err)
	}
	if _, err := svc.GetOrder(ctx, "1"); !errors.Is(err, ErrOrdersNotConfigured) {
		t.Errorf("GetOrder err = %v, want ErrOrdersNotConfigured", err)
	}
}

func TestOrderEventNamesAreIndexedByTheVaultDecoder(t *testing.T) {
	for _, name := range []string{"PurchaseRequested", "PurchaseSettled", "PurchaseRefunded", "PurchaseCancelled", "PurchaseRejected"} {
		found := false
		for _, n := range EventNames {
			found = found || n == name
		}
		if !found {
			t.Errorf("EventNames lacks %s", name)
		}
	}
	for _, name := range []string{"Purchased", "TreasuryChanged", "StrategyChanged", "ProceedsWithdrawn", "MinPurchaseQuoteChanged"} {
		found := false
		for _, n := range EventNames {
			found = found || n == name
		}
		if !found {
			t.Errorf("EventNames lost %s", name)
		}
	}
}

// the logs below are packed from the contract ABI; values above 2^64 expose truncation and
// distinct addresses expose swapped topics
func TestDecodeLogPurchaseRequestedMapsEveryField(t *testing.T) {
	vault := bindings.NewVault()
	event := vault.ABI.Events["PurchaseRequested"]
	buyer := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	recipient := common.HexToAddress("0x00000000000000000000000000000000000000c2")
	strategy := common.HexToAddress("0x00000000000000000000000000000000000000d3")
	tokens, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	maxQuote, _ := new(big.Int).SetString("98765432109876543210", 10)
	round, _ := new(big.Int).SetString("36893488147419103233", 10) // 2^65 + 1, a uint80 past uint64
	packed, err := event.Inputs.NonIndexed().Pack(tokens, maxQuote, strategy, round, uint64(1700000000))
	if err != nil {
		t.Fatal(err)
	}
	log := types.Log{
		Topics: []common.Hash{event.ID, common.BigToHash(big.NewInt(42)), common.BytesToHash(buyer.Bytes()), common.BytesToHash(recipient.Bytes())},
		Data:   packed,
	}

	name, data, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "PurchaseRequested" {
		t.Fatalf("name = %q", name)
	}
	want := map[string]any{
		"id":             "42",
		"buyer":          buyer.Hex(),
		"recipient":      recipient.Hex(),
		"strategy":       strategy.Hex(),
		"tokenAmount":    "123456789012345678901234567890",
		"maxQuoteAmount": "98765432109876543210",
		"baseRound":      "36893488147419103233",
		"createdAt":      uint64(1700000000),
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("%s = %v (%T), want %v (%T)", k, data[k], data[k], v, v)
		}
	}
	if len(data) != len(want) {
		t.Errorf("data has %d keys, want %d: %v", len(data), len(want), data)
	}
}

func TestDecodeLogPurchasePricedAndClosedEvents(t *testing.T) {
	vault := bindings.NewVault()
	buyer := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	round, _ := new(big.Int).SetString("36893488147419103233", 10)
	id := common.BigToHash(big.NewInt(42))

	for _, name := range []string{"PurchaseSettled", "PurchaseRefunded"} {
		event := vault.ABI.Events[name]
		packed, err := event.Inputs.NonIndexed().Pack(round, big.NewInt(2400), big.NewInt(100))
		if err != nil {
			t.Fatal(err)
		}
		gotName, data, err := DecodeLog(types.Log{Topics: []common.Hash{event.ID, id}, Data: packed})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"id": "42", "roundId": "36893488147419103233", "quoteAmount": "2400", "refund": "100"}
		if gotName != name || len(data) != len(want) {
			t.Fatalf("%s decoded as %q with %v", name, gotName, data)
		}
		for k, v := range want {
			if data[k] != v {
				t.Errorf("%s %s = %v, want %v", name, k, data[k], v)
			}
		}
	}
	for _, name := range []string{"PurchaseCancelled", "PurchaseRejected"} {
		event := vault.ABI.Events[name]
		packed, err := event.Inputs.NonIndexed().Pack(big.NewInt(2500))
		if err != nil {
			t.Fatal(err)
		}
		gotName, data, err := DecodeLog(types.Log{Topics: []common.Hash{event.ID, id, common.BytesToHash(buyer.Bytes())}, Data: packed})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"id": "42", "buyer": buyer.Hex(), "refund": "2500"}
		if gotName != name || len(data) != len(want) {
			t.Fatalf("%s decoded as %q with %v", name, gotName, data)
		}
		for k, v := range want {
			if data[k] != v {
				t.Errorf("%s %s = %v, want %v", name, k, data[k], v)
			}
		}
	}
}

func TestDecodeLogPurchaseEventsRejectTruncatedData(t *testing.T) {
	vault := bindings.NewVault()
	for _, name := range []string{"PurchaseRequested", "PurchaseSettled", "PurchaseRefunded", "PurchaseCancelled", "PurchaseRejected"} {
		event := vault.ABI.Events[name]
		log := types.Log{Topics: []common.Hash{event.ID, {}, {}, {}}, Data: []byte{1, 2}}
		if _, _, err := DecodeLog(log); err == nil {
			t.Errorf("DecodeLog accepted a truncated %s log", name)
		}
	}
}
