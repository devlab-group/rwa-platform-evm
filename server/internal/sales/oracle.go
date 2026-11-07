package sales

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
)

// Strategy kinds reported as Project.strategyKind
const (
	KindFixed   = "Fixed"
	KindOracle  = "Oracle"
	KindUnknown = "Unknown"
)

// AlertPriceFeedStale marks Oracle price reverts; transport failures are skipped
const AlertPriceFeedStale = "price_feed_stale"

// OracleInfo describes the Vault's OraclePriceStrategy: its immutable parameters and the
// outcome of a price read made now
type OracleInfo struct {
	Strategy            common.Address
	Feed                common.Address
	MaxAge              uint64
	PurchaseSpreadBps   uint16
	RedemptionSpreadBps uint16
	FeedDecimals        uint8
	// SequencerUptimeFeed is the zero address and GracePeriod 0 without a sequencer feed
	SequencerUptimeFeed common.Address
	GracePeriod         uint64
	// DealingCutoff is the rwa-v4 forward-pricing cut-off in seconds; nil for an rwa-v3
	// strategy, which has none
	DealingCutoff *uint64
	// Available reports whether latestPrice() succeeded; Price is then the validated feed
	// answer in feed units
	Available bool
	Price     *big.Int
	// UpdatedAt is the feed's last update in Unix seconds, also when stale; nil when the
	// feed cannot be read
	UpdatedAt *int64
	// Error is a bindings.PriceErr* name when Available is false
	Error string
	// TransportFailure marks non-revert latestPrice() failures, which cannot establish feed health
	TransportFailure bool
}

// StaleWithin reports whether a StalePrice failure has a readable feed age below a positive sla.
// This lets alerts tolerate scheduled staleness when maxAge is shorter than the update interval.
func (o *OracleInfo) StaleWithin(sla time.Duration, now time.Time) bool {
	if sla <= 0 || o.Error != bindings.PriceErrStalePrice || o.UpdatedAt == nil {
		return false
	}
	return now.Sub(time.Unix(*o.UpdatedAt, 0)) < sla
}

// StrategyPrices are the per-whole-token prices of a strategy read now. A price is nil when
// its read failed, and Error then names why.
type StrategyPrices struct {
	Purchase, Redemption *big.Int
	Error                string
}

// StrategyKind probes feed() for Oracle, then purchasePricePerWholeToken() for Fixed only
// after a revert. It caches resolved kinds by address and retries inconclusive KindUnknown
// results, including transport failures.
func (s *Service) StrategyKind(ctx context.Context, strategy common.Address) string {
	if v, ok := s.kinds.Load(strategy); ok {
		return v.(string)
	}
	kind := KindUnknown
	if _, err := s.callOracle(ctx, strategy, "feed"); err == nil {
		kind = KindOracle
	} else if blockchain.IsRevert(err) {
		if _, err := s.callOracle(ctx, strategy, "purchasePricePerWholeToken"); err == nil {
			kind = KindFixed
		}
	}
	if kind != KindUnknown {
		s.kinds.Store(strategy, kind)
	}
	return kind
}

func (s *Service) callOracle(ctx context.Context, strategy common.Address, method string) ([]any, error) {
	data, err := s.oracle.ABI.Pack(method)
	if err != nil {
		return nil, err
	}
	raw, err := blockchain.Call(ctx, s.client, strategy, data)
	if err != nil {
		return nil, err
	}
	return s.oracle.ABI.Unpack(method, raw)
}

// oracleParams are the immutable OraclePriceStrategy views, read once per address
type oracleParams struct {
	feed, sequencer        common.Address
	maxAge, gracePeriod    uint64
	purchaseBps, redeemBps uint16
	feedDecimals           uint8
	// dealingCutoff is nil for an rwa-v3 strategy, whose dealingCutoff() reverts
	dealingCutoff *uint64
}

func (s *Service) oracleParams(ctx context.Context, strategy common.Address) (oracleParams, error) {
	if v, ok := s.oracleCfg.Load(strategy); ok {
		return v.(oracleParams), nil
	}
	var p oracleParams
	read := func(method string, dst any) error {
		out, err := s.callOracle(ctx, strategy, method)
		if err != nil {
			return fmt.Errorf("sales: strategy.%s: %w", method, err)
		}
		abi.ConvertType(out[0], dst)
		return nil
	}
	for _, step := range []struct {
		method string
		dst    any
	}{
		{"feed", &p.feed},
		{"sequencerUptimeFeed", &p.sequencer},
		{"maxAge", &p.maxAge},
		{"gracePeriod", &p.gracePeriod},
		{"purchaseSpreadBps", &p.purchaseBps},
		{"redemptionSpreadBps", &p.redeemBps},
		{"feedDecimals", &p.feedDecimals},
	} {
		if err := read(step.method, step.dst); err != nil {
			return oracleParams{}, err
		}
	}
	// Treat a reverted dealingCutoff read as an absent view. Return other errors so a temporary
	// RPC failure does not cache a missing cutoff.
	if out, err := s.callOracle(ctx, strategy, "dealingCutoff"); err == nil {
		var cutoff uint64
		abi.ConvertType(out[0], &cutoff)
		p.dealingCutoff = &cutoff
	} else if !blockchain.IsRevert(err) {
		return oracleParams{}, fmt.Errorf("sales: strategy.dealingCutoff: %w", err)
	}
	s.oracleCfg.Store(strategy, p)
	return p, nil
}

// readFailure maps a failed strategy read to an OraclePrice.error name
func (s *Service) readFailure(err error) string {
	return s.oracle.PriceErrorName(bindings.RevertData(err))
}

// Oracle reads the OraclePriceStrategy at strategy: its parameters, latestPrice() and the
// feed's own updatedAt. An error means the parameters could not be read; a failing price
// read is reported in OracleInfo.Error instead.
func (s *Service) Oracle(ctx context.Context, strategy common.Address) (*OracleInfo, error) {
	p, err := s.oracleParams(ctx, strategy)
	if err != nil {
		return nil, err
	}
	info := &OracleInfo{
		Strategy:            strategy,
		Feed:                p.feed,
		MaxAge:              p.maxAge,
		PurchaseSpreadBps:   p.purchaseBps,
		RedemptionSpreadBps: p.redeemBps,
		FeedDecimals:        p.feedDecimals,
		SequencerUptimeFeed: p.sequencer,
		GracePeriod:         p.gracePeriod,
		DealingCutoff:       p.dealingCutoff,
	}

	if data, err := s.oracle.ABI.Pack("latestPrice"); err == nil {
		if raw, err := blockchain.Call(ctx, s.client, strategy, data); err != nil {
			info.Error, info.TransportFailure = s.readFailure(err), !blockchain.IsRevert(err)
		} else if lp, err := s.oracle.UnpackLatestPrice(raw); err != nil {
			info.Error = bindings.PriceErrReadFailed
		} else {
			info.Available, info.Price = true, lp.Price
		}
	}

	if data, err := s.aggregator.ABI.Pack("latestRoundData"); err == nil {
		if raw, err := blockchain.Call(ctx, s.client, p.feed, data); err == nil {
			if at, err := s.aggregator.UnpackUpdatedAt(raw); err == nil && at.IsInt64() {
				v := at.Int64()
				info.UpdatedAt = &v
			}
		}
	}
	return info, nil
}

// CurrentOracle returns the Vault's OracleInfo, or nil when its current strategy is not an
// OraclePriceStrategy
func (s *Service) CurrentOracle(ctx context.Context) (*OracleInfo, error) {
	strategy, err := s.strategyAddress(ctx)
	if err != nil {
		return nil, err
	}
	if s.StrategyKind(ctx, strategy) != KindOracle {
		return nil, nil
	}
	return s.Oracle(ctx, strategy)
}

// OraclePrices reads the Oracle strategy's purchase and redemption prices; a failed read
// leaves its price nil and sets Error to the first failure's name
func (s *Service) OraclePrices(ctx context.Context, strategy common.Address) StrategyPrices {
	var out StrategyPrices
	for _, m := range []struct {
		method string
		dst    **big.Int
	}{{"purchasePricePerWholeToken", &out.Purchase}, {"redemptionPricePerWholeToken", &out.Redemption}} {
		v, err := s.readStrategyPrice(ctx, strategy, m.method)
		if err != nil {
			if out.Error == "" {
				out.Error = s.readFailure(err)
			}
			continue
		}
		*m.dst = v
	}
	return out
}

// ForwardPriced reads strategy.forwardPriced(), nil when the read fails. rwa-v2 and rwa-v3
// strategies revert, so callers read it for rwa-v4 projects only.
func (s *Service) ForwardPriced(ctx context.Context, strategy common.Address) *bool {
	out, err := s.callOracle(ctx, strategy, "forwardPriced")
	if err != nil {
		return nil
	}
	v := *abi.ConvertType(out[0], new(bool)).(*bool)
	return &v
}

// CurrentStrategy returns the Vault's live strategy address
func (s *Service) CurrentStrategy(ctx context.Context) (common.Address, error) {
	return s.strategyAddress(ctx)
}
