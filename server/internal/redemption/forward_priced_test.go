package redemption

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

// 2^65 + 1, a uint80 round id that does not fit in 64 bits
const bigRound = "36893488147419103233"

func TestDecodeLogTypesTheForwardPricingEvents(t *testing.T) {
	e := bindings.NewRedemptionEscrow()
	idTopic := common.BigToHash(big.NewInt(42))
	strategy := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	round, _ := new(big.Int).SetString(bigRound, 10)

	pack := func(name string, vals ...any) []byte {
		out, err := e.ABI.Events[name].Inputs.NonIndexed().Pack(vals...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cases := []struct {
		name string
		log  types.Log
		want map[string]any
	}{
		{
			"RedemptionForwardPriced",
			types.Log{
				Topics: []common.Hash{e.EventID("RedemptionForwardPriced"), idTopic, common.BytesToHash(strategy.Bytes())},
				Data:   pack("RedemptionForwardPriced", round, big.NewInt(900)),
			},
			map[string]any{"id": "42", "strategy": strategy.Hex(), "baseRound": bigRound, "minQuoteOut": "900"},
		},
		{
			"RedemptionPriced",
			types.Log{Topics: []common.Hash{e.EventID("RedemptionPriced"), idTopic}, Data: pack("RedemptionPriced", round, big.NewInt(950))},
			map[string]any{"id": "42", "roundId": bigRound, "quoteAmount": "950"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, data, err := DecodeLog(tc.log)
			if err != nil {
				t.Fatal(err)
			}
			if name != tc.name {
				t.Fatalf("name = %q, want %q", name, tc.name)
			}
			for k, want := range tc.want {
				if data[k] != want {
					t.Errorf("%s = %v (%T), want %v (%T)", k, data[k], data[k], want, want)
				}
			}
			if len(data) != len(tc.want) {
				t.Errorf("data has %d keys, want %d: %v", len(data), len(tc.want), data)
			}
		})
		t.Run(tc.name+" rejects truncated data", func(t *testing.T) {
			if _, _, err := DecodeLog(types.Log{Topics: tc.log.Topics, Data: []byte{1, 2}}); err == nil {
				t.Error("decoded a truncated log")
			}
		})
	}
}

func TestEventNamesIncludeTheForwardPricingEvents(t *testing.T) {
	have := map[string]bool{}
	for _, n := range EventNames {
		have[n] = true
	}
	for _, n := range []string{"RedemptionRequested", "RedemptionFunded", "RedemptionCompleted", "RedemptionRejected", "RedemptionCancelled", "RedemptionForwardPriced", "RedemptionPriced"} {
		if !have[n] {
			t.Errorf("EventNames lacks %s", n)
		}
	}
}

// forward request: the Requested event carries quoteAmount 0, and the quote is fixed at funding
func forwardRequest() []*models.ChainEvent {
	return []*models.ChainEvent{
		ev("RedemptionRequested", "5", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "0", "createdAt": uint64(1000)}),
		ev("RedemptionForwardPriced", "5", 10, 1, map[string]any{"strategy": "0x57", "baseRound": bigRound, "minQuoteOut": "900"}),
	}
}

func TestBuildStatesForwardRequestIsPendingWithAZeroQuote(t *testing.T) {
	r := BuildStates(forwardRequest(), 1209600)["5"]
	if r.Status != models.RedemptionPending || r.QuoteAmount != "0" {
		t.Errorf("status/quote = %s/%q, want Pending/0", r.Status, r.QuoteAmount)
	}
	if r.Strategy != "0x57" || r.BaseRound != bigRound || r.MinQuoteOut != "900" || r.PricedRound != "" {
		t.Errorf("forward fields = strategy %q base %q min %q priced %q", r.Strategy, r.BaseRound, r.MinQuoteOut, r.PricedRound)
	}
}

func TestBuildStatesFundingPricesAForwardRequest(t *testing.T) {
	events := append(forwardRequest(),
		ev("RedemptionPriced", "5", 12, 0, map[string]any{"roundId": bigRound, "quoteAmount": "950"}),
		ev("RedemptionFunded", "5", 12, 1, map[string]any{"funder": "0xBBB", "quoteAmount": "950"}),
	)
	r := BuildStates(events, 1209600)["5"]
	if r.Status != models.RedemptionFunded || r.QuoteAmount != "950" || r.PricedRound != bigRound {
		t.Errorf("status/quote/round = %s/%q/%q, want Funded/950/%s", r.Status, r.QuoteAmount, r.PricedRound, bigRound)
	}
	// the request-time fields survive pricing
	if r.Strategy != "0x57" || r.BaseRound != bigRound || r.MinQuoteOut != "900" {
		t.Errorf("forward fields lost: %+v", r)
	}

	events = append(events, ev("RedemptionCompleted", "5", 14, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950"}))
	if r := BuildStates(events, 1209600)["5"]; r.Status != models.RedemptionCompleted || r.QuoteAmount != "950" || r.PricedRound != bigRound {
		t.Errorf("completed: %+v", r)
	}
}

func TestBuildStatesPricingEventsApplyToPendingRequestsOnly(t *testing.T) {
	priced := func(b uint64) *models.ChainEvent {
		return ev("RedemptionPriced", "5", b, 0, map[string]any{"roundId": "99", "quoteAmount": "1"})
	}
	late := ev("RedemptionForwardPriced", "5", 20, 0, map[string]any{"strategy": "0xBAD", "baseRound": "1", "minQuoteOut": "1"})
	tests := []struct {
		name   string
		events []*models.ChainEvent
	}{
		{"after cancel", append(forwardRequest(), ev("RedemptionCancelled", "5", 11, 0, nil), priced(12), late)},
		{"after reject", append(forwardRequest(), ev("RedemptionRejected", "5", 11, 0, map[string]any{"reasonCode": "0xdead"}), priced(12), late)},
		{
			"after funding",
			append(forwardRequest(), ev("RedemptionPriced", "5", 11, 0, map[string]any{"roundId": bigRound, "quoteAmount": "950"}),
				ev("RedemptionFunded", "5", 11, 1, nil), priced(12), late),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := BuildStates(tt.events, 1209600)["5"]
			if r.PricedRound == "99" || r.QuoteAmount == "1" || r.Strategy == "0xBAD" {
				t.Errorf("a closed request was repriced: %+v", r)
			}
		})
	}

	t.Run("pricing events for a request that never happened create nothing", func(t *testing.T) {
		states := BuildStates([]*models.ChainEvent{priced(3), late}, 1209600)
		if len(states) != 0 {
			t.Errorf("got %d requests, want 0", len(states))
		}
	})
}

func TestBuildStatesRequestPricedAtRequestTimeHasNoForwardFields(t *testing.T) {
	r := BuildStates([]*models.ChainEvent{
		ev("RedemptionRequested", "1", 10, 0, map[string]any{"beneficiary": "0xAAA", "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1000)}),
		ev("RedemptionFunded", "1", 11, 0, map[string]any{"funder": "0xBBB", "quoteAmount": "950"}),
	}, 1209600)["1"]
	if r.QuoteAmount != "950" || r.Strategy != "" || r.BaseRound != "" || r.MinQuoteOut != "" || r.PricedRound != "" {
		t.Errorf("an instant-priced request: %+v", r)
	}
}

func TestReconcileRebuildsForwardFieldsAndFollowsAReorg(t *testing.T) {
	escrow := common.HexToAddress("0x0000000000000000000000000000000000000E")
	events := memory.NewChainEventRepository()
	repo := memory.NewRedemptionRequestRepository()
	svc := New(nil, escrow, repo, common.Address{})
	ctx := context.Background()
	for _, e := range append(forwardRequest(),
		ev("RedemptionPriced", "5", 12, 0, map[string]any{"roundId": bigRound, "quoteAmount": "950"}),
		ev("RedemptionFunded", "5", 12, 1, map[string]any{"funder": "0xBBB", "quoteAmount": "950"}),
	) {
		e.ChainID, e.Address = 31337, escrow.Hex()
		if err := events.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatal(err)
	}
	if r, _ := svc.Get(ctx, "5"); r == nil || r.Status != models.RedemptionFunded || r.PricedRound != bigRound || r.QuoteAmount != "950" {
		t.Fatalf("request = %+v, want Funded and priced", r)
	}

	// the funding block is reorged out: the request is Pending again and unpriced
	if _, err := events.DeleteFromBlock(ctx, 31337, escrow.Hex(), 12); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(ctx, events, 31337, 1209600); err != nil {
		t.Fatal(err)
	}
	r, _ := svc.Get(ctx, "5")
	if r == nil || r.Status != models.RedemptionPending || r.QuoteAmount != "0" || r.PricedRound != "" || r.BaseRound != bigRound {
		t.Errorf("after the reorg = %+v, want Pending, quote 0, no priced round, base round kept", r)
	}
}
