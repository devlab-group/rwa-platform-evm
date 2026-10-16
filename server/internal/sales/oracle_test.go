package sales

import (
	"context"
	"errors"
	"math/big"
	"testing"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
)

var (
	oVault    = common.HexToAddress("0x000000000000000000000000000000000000000A")
	oQuote    = common.HexToAddress("0x000000000000000000000000000000000000000B")
	oStrategy = common.HexToAddress("0x000000000000000000000000000000000000000C")
	oFeed     = common.HexToAddress("0x000000000000000000000000000000000000FEED")
	oSeq      = common.HexToAddress("0x00000000000000000000000000000000000005E0")
)

// revertErr is an eth_call failure carrying revert data, as go-ethereum's rpc.DataError does
type revertErr struct{ data string }

func (e revertErr) Error() string          { return "execution reverted" }
func (e revertErr) ErrorData() interface{} { return e.data }

func errSelector(sig string, args ...any) revertErr {
	data := crypto.Keccak256([]byte(sig))[:4]
	return revertErr{data: "0x" + common.Bytes2Hex(data) + common.Bytes2Hex(make([]byte, 32*len(args)))}
}

// scripted answers every eth_call from a handler keyed by (contract, method name)
type scripted struct {
	blockchain.Client
	calls   int
	answers map[common.Address]map[string]any // value, or an error to return
	abis    []abi.ABI
}

func (s *scripted) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	s.calls++
	for _, a := range s.abis {
		m, err := a.MethodById(msg.Data[:4])
		if err != nil {
			continue
		}
		v, ok := s.answers[*msg.To][m.Name]
		if !ok {
			return nil, revertErr{data: "0x"}
		}
		if e, isErr := v.(error); isErr {
			return nil, e
		}
		if vals, isTuple := v.([]any); isTuple {
			return m.Outputs.Pack(vals...)
		}
		return m.Outputs.Pack(v)
	}
	return nil, errors.New("scripted: unknown selector")
}

func newScripted(answers map[common.Address]map[string]any) *scripted {
	return &scripted{answers: answers, abis: []abi.ABI{
		bindings.NewOraclePriceStrategy().ABI, bindings.NewAggregatorV3().ABI, bindings.NewVault().ABI,
		bindings.NewERC20().ABI, bindings.NewFixedPriceStrategy().ABI,
	}}
}

func oracleAnswers() map[common.Address]map[string]any {
	return map[common.Address]map[string]any{
		oVault: {"strategy": oStrategy, "inventory": big.NewInt(500)},
		oQuote: {"balanceOf": big.NewInt(900)},
		oStrategy: {
			"feed": oFeed, "sequencerUptimeFeed": oSeq, "maxAge": uint64(3600), "gracePeriod": uint64(900),
			"purchaseSpreadBps": uint16(25), "redemptionSpreadBps": uint16(40), "feedDecimals": uint8(8),
			"latestPrice":                  []any{big.NewInt(150_000_000), big.NewInt(1_700_000_000)},
			"purchasePricePerWholeToken":   big.NewInt(1_503_750),
			"redemptionPricePerWholeToken": big.NewInt(1_494_000),
		},
		oFeed: {"latestRoundData": []any{big.NewInt(1), big.NewInt(150_000_000), big.NewInt(1_699_999_000), big.NewInt(1_700_000_000), big.NewInt(1)}},
	}
}

func newOracleService(c *scripted) *Service {
	return New(c, oVault, oQuote, memory.NewPurchaseRepository())
}

func TestStrategyKindProbe(t *testing.T) {
	fixedStrategy := map[string]any{"purchasePricePerWholeToken": big.NewInt(1)} // feed() reverts: not in the map

	cases := []struct {
		name    string
		answers map[common.Address]map[string]any
		want    string
	}{
		{"oracle", map[common.Address]map[string]any{oStrategy: {"feed": oFeed}}, KindOracle},
		{"oracle whose price currently reverts is still an oracle", map[common.Address]map[string]any{oStrategy: {"feed": oFeed, "purchasePricePerWholeToken": errSelector("StalePrice(uint256,uint256)", 1, 2)}}, KindOracle},
		{"fixed", map[common.Address]map[string]any{oStrategy: fixedStrategy}, KindFixed},
		{"neither view answers", map[common.Address]map[string]any{oStrategy: {}}, KindUnknown},
		{"fixed lookalike whose price reverts", map[common.Address]map[string]any{oStrategy: {"purchasePricePerWholeToken": errSelector("Anything()")}}, KindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newOracleService(newScripted(tc.answers))
			if got := svc.StrategyKind(context.Background(), oStrategy); got != tc.want {
				t.Errorf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStrategyKindTransportFailureIsNotCached(t *testing.T) {
	c := newScripted(map[common.Address]map[string]any{oStrategy: {"feed": errors.New("dial tcp: connection refused")}})
	svc := newOracleService(c)
	if got := svc.StrategyKind(context.Background(), oStrategy); got != KindUnknown {
		t.Fatalf("kind = %q, want Unknown on a transport failure", got)
	}
	// a node that is reachable again must not be answered from a stale Unknown
	c.answers[oStrategy]["feed"] = oFeed
	if got := svc.StrategyKind(context.Background(), oStrategy); got != KindOracle {
		t.Errorf("kind after recovery = %q", got)
	}
	// a positive answer is cached: no further calls
	calls := c.calls
	if got := svc.StrategyKind(context.Background(), oStrategy); got != KindOracle || c.calls != calls {
		t.Errorf("cached kind = %q with %d extra calls", got, c.calls-calls)
	}
}

func TestOracleReadsParametersPriceAndFeedTimestamp(t *testing.T) {
	svc := newOracleService(newScripted(oracleAnswers()))
	info, err := svc.Oracle(context.Background(), oStrategy)
	if err != nil {
		t.Fatal(err)
	}
	if info.Feed != oFeed || info.MaxAge != 3600 || info.PurchaseSpreadBps != 25 || info.RedemptionSpreadBps != 40 ||
		info.FeedDecimals != 8 || info.SequencerUptimeFeed != oSeq || info.GracePeriod != 900 {
		t.Errorf("parameters = %+v", info)
	}
	if !info.Available || info.Price.Int64() != 150_000_000 || info.Error != "" {
		t.Errorf("price: available=%v price=%v error=%q", info.Available, info.Price, info.Error)
	}
	if info.UpdatedAt == nil || *info.UpdatedAt != 1_700_000_000 {
		t.Errorf("updatedAt = %v, want the feed's 1700000000", info.UpdatedAt)
	}
}

func TestOracleErrorsAreDecodedFromTheRevertSelector(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"stale", errSelector("StalePrice(uint256,uint256)", 1, 2), bindings.PriceErrStalePrice},
		{"invalid", errSelector("InvalidPrice(int256,uint256)", 1, 2), bindings.PriceErrInvalidPrice},
		{"sequencer down", errSelector("SequencerDown()"), bindings.PriceErrSequencerDown},
		{"grace period", errSelector("GracePeriodNotOver(uint256,uint256)", 1, 2), bindings.PriceErrGracePeriodNotOver},
		{"feed decimals", errSelector("FeedDecimalsChanged(uint8,uint8)", 1, 2), bindings.PriceErrFeedDecimalsChanged},
		{"unknown revert", errSelector("Surprise()"), bindings.PriceErrReadFailed},
		{"empty revert", revertErr{data: "0x"}, bindings.PriceErrReadFailed},
		{"rpc failure", errors.New("dial tcp: i/o timeout"), bindings.PriceErrReadFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := oracleAnswers()
			answers[oStrategy]["latestPrice"] = tc.err
			info, err := newOracleService(newScripted(answers)).Oracle(context.Background(), oStrategy)
			if err != nil {
				t.Fatal(err)
			}
			if info.Available || info.Price != nil || info.Error != tc.want {
				t.Errorf("available=%v price=%v error=%q, want error %q", info.Available, info.Price, info.Error, tc.want)
			}
			// the feed's timestamp is still reported when the price is stale
			if info.UpdatedAt == nil || *info.UpdatedAt != 1_700_000_000 {
				t.Errorf("updatedAt = %v on an unavailable price", info.UpdatedAt)
			}
		})
	}
}

func TestOracleOmitsUpdatedAtWhenTheFeedCannotBeRead(t *testing.T) {
	answers := oracleAnswers()
	answers[oFeed]["latestRoundData"] = errors.New("feed offline")
	info, err := newOracleService(newScripted(answers)).Oracle(context.Background(), oStrategy)
	if err != nil {
		t.Fatal(err)
	}
	if info.UpdatedAt != nil || !info.Available {
		t.Errorf("updatedAt=%v available=%v", info.UpdatedAt, info.Available)
	}
}

func TestOracleFailsWhenParametersCannotBeRead(t *testing.T) {
	answers := oracleAnswers()
	answers[oStrategy]["maxAge"] = errors.New("rpc down")
	svc := newOracleService(newScripted(answers))
	if _, err := svc.Oracle(context.Background(), oStrategy); err == nil {
		t.Fatal("unreadable immutables must be an error, not a half-filled OracleInfo")
	}
}

func TestOracleParametersAreReadOnce(t *testing.T) {
	c := newScripted(oracleAnswers())
	svc := newOracleService(c)
	if _, err := svc.Oracle(context.Background(), oStrategy); err != nil {
		t.Fatal(err)
	}
	first := c.calls
	if _, err := svc.Oracle(context.Background(), oStrategy); err != nil {
		t.Fatal(err)
	}
	// the second read repeats only latestPrice and latestRoundData
	if c.calls-first != 2 {
		t.Errorf("second Oracle read made %d calls, want 2", c.calls-first)
	}
}

func TestGetInventoryOracle(t *testing.T) {
	t.Run("prices from the strategy", func(t *testing.T) {
		inv, err := newOracleService(newScripted(oracleAnswers())).GetInventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inv.PurchasePrice != "1503750" || inv.RedemptionPrice != "1494000" || inv.PriceError != "" || inv.Inventory != "500" || inv.QuoteBalance != "900" {
			t.Errorf("inventory = %+v", inv)
		}
	})
	t.Run("a stale feed omits the prices and says why", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oStrategy]["purchasePricePerWholeToken"] = errSelector("StalePrice(uint256,uint256)", 1, 2)
		answers[oStrategy]["redemptionPricePerWholeToken"] = errSelector("StalePrice(uint256,uint256)", 1, 2)
		inv, err := newOracleService(newScripted(answers)).GetInventory(context.Background())
		if err != nil {
			t.Fatalf("an oracle read failure must not fail the whole inventory: %v", err)
		}
		if inv.PurchasePrice != "" || inv.RedemptionPrice != "" || inv.PriceError != bindings.PriceErrStalePrice {
			t.Errorf("inventory = %+v", inv)
		}
		if inv.Inventory != "500" {
			t.Errorf("inventory figures must survive: %+v", inv)
		}
	})
	t.Run("only one price fails", func(t *testing.T) {
		answers := oracleAnswers()
		answers[oStrategy]["redemptionPricePerWholeToken"] = errors.New("rpc hiccup")
		inv, err := newOracleService(newScripted(answers)).GetInventory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if inv.PurchasePrice != "1503750" || inv.RedemptionPrice != "" || inv.PriceError != bindings.PriceErrReadFailed {
			t.Errorf("inventory = %+v", inv)
		}
	})
}

func TestCurrentOracle(t *testing.T) {
	info, err := newOracleService(newScripted(oracleAnswers())).CurrentOracle(context.Background())
	if err != nil || info == nil || info.Strategy != oStrategy {
		t.Fatalf("oracle strategy: %+v %v", info, err)
	}

	fixed := oracleAnswers()
	fixed[oStrategy] = map[string]any{"purchasePricePerWholeToken": big.NewInt(1)}
	if info, err := newOracleService(newScripted(fixed)).CurrentOracle(context.Background()); err != nil || info != nil {
		t.Errorf("fixed strategy: %+v %v", info, err)
	}

	broken := oracleAnswers()
	broken[oVault]["strategy"] = errors.New("rpc down")
	if _, err := newOracleService(newScripted(broken)).CurrentOracle(context.Background()); err == nil {
		t.Error("an unreadable Vault.strategy must surface as an error")
	}
}
