package sales

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestInventoryReportsOpenOrderDeposits(t *testing.T) {
	t.Run("an rwa-v4 vault reports its pending deposits", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oVault]["pendingPurchaseQuote"] = big.NewInt(123456789012)
		inv, err := newOracleService(newScripted(answers)).GetInventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inv.PendingPurchaseQuote != "123456789012" || inv.QuoteBalance != "900" {
			t.Errorf("inventory = %+v, want pendingPurchaseQuote 123456789012 beside the balance", inv)
		}
	})
	t.Run("no open orders reports zero rather than omitting", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oVault]["pendingPurchaseQuote"] = big.NewInt(0)
		inv, err := newOracleService(newScripted(answers)).GetInventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(inv)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["pendingPurchaseQuote"] != "0" {
			t.Errorf("JSON pendingPurchaseQuote = %v, want \"0\" (%s)", m["pendingPurchaseQuote"], raw)
		}
	})
	t.Run("an older vault that reverts is omitted", func(t *testing.T) {
		inv, err := newOracleService(newScripted(oracleAnswers())).GetInventory(context.Background())
		if err != nil {
			t.Fatalf("a Vault without pendingPurchaseQuote must still serve its inventory: %v", err)
		}
		raw, _ := json.Marshal(inv)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if _, has := m["pendingPurchaseQuote"]; has || inv.Inventory != "500" {
			t.Errorf("inventory JSON = %s, want pendingPurchaseQuote omitted and the rest intact", raw)
		}
	})
	t.Run("a transport failure is an error, not an omission", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oVault]["pendingPurchaseQuote"] = errors.New("dial tcp: i/o timeout")
		if _, err := newOracleService(newScripted(answers)).GetInventory(context.Background()); err == nil {
			t.Fatal("GetInventory hid a failed pendingPurchaseQuote read behind an omission")
		}
	})
	t.Run("the same holds on a fixed strategy", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oVault]["pendingPurchaseQuote"] = errors.New("dial tcp: i/o timeout")
		answers[oStrategy] = map[string]any{
			"purchasePricePerWholeToken":   big.NewInt(2),
			"redemptionPricePerWholeToken": big.NewInt(1),
		}
		if _, err := newOracleService(newScripted(answers)).GetInventory(context.Background()); err == nil {
			t.Fatal("GetInventory hid a failed pendingPurchaseQuote read behind an omission")
		}
	})
}

func TestOracleReadsTheDealingCutoffOnceAndOmitsItForRwaV3(t *testing.T) {
	t.Run("rwa-v4 strategy", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oStrategy]["dealingCutoff"] = uint64(7200)
		c := newScripted(answers)
		svc := newOracleService(c)
		info, err := svc.Oracle(context.Background(), oStrategy)
		if err != nil {
			t.Fatal(err)
		}
		if info.DealingCutoff == nil || *info.DealingCutoff != 7200 {
			t.Fatalf("DealingCutoff = %v, want 7200", info.DealingCutoff)
		}
		first := c.calls
		if _, err := svc.Oracle(context.Background(), oStrategy); err != nil {
			t.Fatal(err)
		}
		if c.calls-first != 2 {
			t.Errorf("second read made %d calls, want 2: the cut-off is immutable and cached with the other parameters", c.calls-first)
		}
	})
	t.Run("rwa-v3 strategy reverts and has none", func(t *testing.T) {
		info, err := newOracleService(newScripted(oracleAnswers())).Oracle(context.Background(), oStrategy)
		if err != nil {
			t.Fatal(err)
		}
		if info.DealingCutoff != nil {
			t.Errorf("DealingCutoff = %d, want nil for a strategy without the view", *info.DealingCutoff)
		}
	})
	t.Run("a zero cut-off is reported, not omitted", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oStrategy]["dealingCutoff"] = uint64(0)
		info, err := newOracleService(newScripted(answers)).Oracle(context.Background(), oStrategy)
		if err != nil || info.DealingCutoff == nil || *info.DealingCutoff != 0 {
			t.Errorf("DealingCutoff = %v, err = %v; want a present zero", info.DealingCutoff, err)
		}
	})
	t.Run("a transport failure is not cached as no cut-off", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oStrategy]["dealingCutoff"] = errors.New("dial tcp: connection refused")
		c := newScripted(answers)
		svc := newOracleService(c)
		if _, err := svc.Oracle(context.Background(), oStrategy); err == nil {
			t.Fatal("Oracle succeeded although dealingCutoff() could not be read")
		}
		// the node recovers: the parameters must be read again, not served from a cached omission
		c.answers[oStrategy]["dealingCutoff"] = uint64(60)
		info, err := svc.Oracle(context.Background(), oStrategy)
		if err != nil || info.DealingCutoff == nil || *info.DealingCutoff != 60 {
			t.Errorf("after recovery: cutoff = %v, err = %v; want 60", info.DealingCutoff, err)
		}
	})
}

func TestForwardPriced(t *testing.T) {
	fixed := common.HexToAddress("0x00000000000000000000000000000000000000F1")
	answers := oracleAnswers()
	answers[oStrategy]["forwardPriced"] = true
	answers[fixed] = map[string]any{"forwardPriced": false}
	answers[common.HexToAddress("0x00000000000000000000000000000000000000F2")] = map[string]any{"forwardPriced": errors.New("rpc down")}
	svc := newOracleService(newScripted(answers))

	for name, tc := range map[string]struct {
		strategy common.Address
		want     *bool
	}{
		"oracle prices forward":       {oStrategy, boolPtr(true)},
		"fixed strategy is instant":   {fixed, boolPtr(false)},
		"failed read is unknown":      {common.HexToAddress("0x00000000000000000000000000000000000000F2"), nil},
		"strategy without the getter": {common.HexToAddress("0x00000000000000000000000000000000000000F3"), nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := svc.ForwardPriced(context.Background(), tc.strategy)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("ForwardPriced = %v, want %v", deref(got), deref(tc.want))
			}
		})
	}
}

func TestStrategyKindStillIdentifiesAnRwaV4OracleStrategy(t *testing.T) {
	answers := oracleAnswers()
	answers[oStrategy]["dealingCutoff"] = uint64(7200)
	answers[oStrategy]["forwardPriced"] = true
	svc := newOracleService(newScripted(answers))
	if got := svc.StrategyKind(context.Background(), oStrategy); got != KindOracle {
		t.Errorf("kind = %q, want Oracle", got)
	}
	// and a v4 fixed strategy, which now also answers forwardPriced(), is still Fixed
	fixed := common.HexToAddress("0x00000000000000000000000000000000000000F1")
	answers[fixed] = map[string]any{"purchasePricePerWholeToken": big.NewInt(1), "forwardPriced": false}
	svc = newOracleService(newScripted(answers))
	if got := svc.StrategyKind(context.Background(), fixed); got != KindFixed {
		t.Errorf("kind = %q, want Fixed", got)
	}
}

func boolPtr(b bool) *bool { return &b }

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
