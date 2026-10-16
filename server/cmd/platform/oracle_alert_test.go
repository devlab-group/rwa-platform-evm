package main

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"testing/synctest"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/sales"
)

var (
	alertVault    = common.HexToAddress("0x000000000000000000000000000000000000000A")
	alertStrategy = common.HexToAddress("0x000000000000000000000000000000000000000C")
	alertFeed     = common.HexToAddress("0x000000000000000000000000000000000000FEED")
)

type chainRevert struct{ data string }

func (e chainRevert) Error() string          { return "execution reverted" }
func (e chainRevert) ErrorData() interface{} { return e.data }

// oracleChain answers the Vault, an OraclePriceStrategy and its feed; price decides what
// latestPrice() does
type oracleChain struct {
	blockchain.Client
	oracleKind bool
	price      error // nil: a fresh price
}

func (c oracleChain) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	oracle, vault, agg := bindings.NewOraclePriceStrategy(), bindings.NewVault().ABI, bindings.NewAggregatorV3()
	find := func(a abi.ABI) (*abi.Method, bool) {
		m, err := a.MethodById(msg.Data[:4])
		return m, err == nil
	}
	if m, ok := find(vault); ok && m.Name == "strategy" {
		return m.Outputs.Pack(alertStrategy)
	}
	if m, ok := find(agg.ABI); ok && *msg.To == alertFeed {
		return m.Outputs.Pack(big.NewInt(1), big.NewInt(1), big.NewInt(1), big.NewInt(1_700_000_000), big.NewInt(1))
	}
	if m, ok := find(oracle.ABI); ok && *msg.To == alertStrategy {
		if !c.oracleKind {
			if m.Name == "purchasePricePerWholeToken" {
				return m.Outputs.Pack(big.NewInt(1))
			}
			return nil, chainRevert{data: "0x"}
		}
		switch m.Name {
		case "feed":
			return m.Outputs.Pack(alertFeed)
		case "sequencerUptimeFeed":
			return m.Outputs.Pack(common.Address{})
		case "maxAge", "gracePeriod":
			return m.Outputs.Pack(uint64(3600))
		case "purchaseSpreadBps", "redemptionSpreadBps":
			return m.Outputs.Pack(uint16(0))
		case "feedDecimals":
			return m.Outputs.Pack(uint8(8))
		case "latestPrice":
			if c.price != nil {
				return nil, c.price
			}
			return m.Outputs.Pack(big.NewInt(1), big.NewInt(1))
		}
	}
	return nil, errors.New("unscripted call")
}

func staleErr(t *testing.T) error {
	t.Helper()
	id := bindings.NewOraclePriceStrategy().ABI.Errors["StalePrice"].ID
	return chainRevert{data: "0x" + common.Bytes2Hex(id[:4]) + "00"}
}

func TestEvaluateAlerts_PriceFeedStale(t *testing.T) {
	cases := []struct {
		name   string
		chain  oracleChain
		alerts int
	}{
		{"fresh oracle price", oracleChain{oracleKind: true}, 0},
		{"stale oracle price", oracleChain{oracleKind: true, price: staleErr(t)}, 1},
		{"sequencer down", oracleChain{oracleKind: true, price: func() error {
			id := bindings.NewOraclePriceStrategy().ABI.Errors["SequencerDown"].ID
			return chainRevert{data: "0x" + common.Bytes2Hex(id[:4])}
		}()}, 1},
		{"rpc failure while reading the price is skipped", oracleChain{oracleKind: true, price: errors.New("i/o timeout")}, 0},
		{"revert that is not a strategy error", oracleChain{oracleKind: true, price: chainRevert{data: "0x"}}, 1},
		{"fixed strategy never alerts", oracleChain{oracleKind: false, price: staleErr(t)}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, repos := guardTestApp(t, 0, nil)
			app.Sales = sales.New(tc.chain, alertVault, common.HexToAddress("0xB"), repos.Purchases)
			evaluateAlerts(context.Background(), config.Config{}, repos, app)
			got := alertTargets(t, repos, sales.AlertPriceFeedStale)
			if len(got) != tc.alerts {
				t.Fatalf("price_feed_stale alerts = %v, want %d", got, tc.alerts)
			}
			if tc.alerts == 1 && got[0] != alertStrategy.Hex() {
				t.Errorf("alert target = %q, want the strategy %s", got[0], alertStrategy.Hex())
			}
		})
	}
}

func TestEvaluateAlerts_PriceFeedStaleNeedsAChainRead(t *testing.T) {
	app, repos := guardTestApp(t, 0, nil)
	// no sales service: nothing to evaluate and nothing to crash on
	evaluateAlerts(context.Background(), config.Config{}, repos, app)
	if got := alertTargets(t, repos, sales.AlertPriceFeedStale); len(got) != 0 {
		t.Errorf("alerts = %v", got)
	}

	// the Vault cannot be read: no alert, since nothing is known about the feed
	app.Sales = sales.New(brokenChain{}, alertVault, common.HexToAddress("0xB"), repos.Purchases)
	evaluateAlerts(context.Background(), config.Config{}, repos, app)
	if got := alertTargets(t, repos, sales.AlertPriceFeedStale); len(got) != 0 {
		t.Errorf("alerts = %v after an unreadable vault", got)
	}
}

type brokenChain struct{ blockchain.Client }

func (brokenChain) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return nil, errors.New("rpc down")
}

// addressesFromProject must keep income in the indexer's watch set
func TestAddressesFromProjectKeepsTheIncomeDistributor(t *testing.T) {
	p := &models.Project{Addresses: models.Addresses{Token: "0xT001", Strategy: "0xST01", IncomeDistributor: "0xINC1"}}
	p.Security = &models.SecurityState{Strategy: "0xST02"}
	got, _ := addressesFromProject(p)
	if got.IncomeDistributor != "0xINC1" || got.Strategy != "0xST02" {
		t.Errorf("addresses = %+v", got)
	}
}

// slowFeedChain is oracleChain with a feed that answers only once the read deadline passes,
// the way a hung RPC does
type slowFeedChain struct{ oracleChain }

func (c slowFeedChain) CallContract(ctx context.Context, msg ethereum.CallMsg, n *big.Int) ([]byte, error) {
	if *msg.To == alertFeed {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.oracleChain.CallContract(ctx, msg, n)
}

// ctxAuditLogs refuses writes on a finished context, as the Mongo store does
type ctxAuditLogs struct{ repository.AuditLogRepository }

func (r ctxAuditLogs) Append(ctx context.Context, e *models.AuditLogEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.AuditLogRepository.Append(ctx, e)
}

// The oracle reads' timeout bounds only the reads: when they use it up, the price_feed_stale
// audit entry is still written
func TestEvaluateAlerts_PriceFeedStaleAuditSurvivesSlowReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app, repos := guardTestApp(t, 0, nil)
		app.Audit = auditlog.New(ctxAuditLogs{repos.AuditLogs})
		app.Sales = sales.New(slowFeedChain{oracleChain{oracleKind: true, price: staleErr(t)}}, alertVault, common.HexToAddress("0xB"), repos.Purchases)

		evaluateAlerts(context.Background(), config.Config{}, repos, app)

		if got := alertTargets(t, repos, sales.AlertPriceFeedStale); len(got) != 1 || got[0] != alertStrategy.Hex() {
			t.Errorf("price_feed_stale alerts = %v, want one for %s", got, alertStrategy.Hex())
		}
	})
}
