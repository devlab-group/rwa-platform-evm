package bindings

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// oraclePriceStrategyABIJSON declares the immutable OraclePriceStrategy views and its
// price-read errors. The contract has no AccessControl and emits no events.
// aggregatorV3ABIJSON holds the two Chainlink feed reads the server needs.
const oraclePriceStrategyABIJSON = `[
  {"type":"function","name":"feed","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"sequencerUptimeFeed","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"maxAge","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint64"}]},
  {"type":"function","name":"gracePeriod","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint64"}]},
  {"type":"function","name":"purchaseSpreadBps","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint16"}]},
  {"type":"function","name":"redemptionSpreadBps","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint16"}]},
  {"type":"function","name":"tokenDecimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"function","name":"quoteDecimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"function","name":"feedDecimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"function","name":"latestPrice","stateMutability":"view","inputs":[],"outputs":[
    {"name":"price","type":"uint256"},{"name":"updatedAt","type":"uint256"}]},
  {"type":"function","name":"purchasePricePerWholeToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"redemptionPricePerWholeToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"error","name":"StalePrice","inputs":[{"name":"updatedAt","type":"uint256"},{"name":"maxAge","type":"uint256"}]},
  {"type":"error","name":"InvalidPrice","inputs":[{"name":"answer","type":"int256"},{"name":"updatedAt","type":"uint256"}]},
  {"type":"error","name":"SequencerDown","inputs":[]},
  {"type":"error","name":"GracePeriodNotOver","inputs":[{"name":"startedAt","type":"uint256"},{"name":"gracePeriod","type":"uint256"}]},
  {"type":"error","name":"FeedDecimalsChanged","inputs":[{"name":"expected","type":"uint8"},{"name":"actual","type":"uint8"}]}
]`

const aggregatorV3ABIJSON = `[
  {"type":"function","name":"decimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"function","name":"latestRoundData","stateMutability":"view","inputs":[],"outputs":[
    {"name":"roundId","type":"uint80"},{"name":"answer","type":"int256"},{"name":"startedAt","type":"uint256"},
    {"name":"updatedAt","type":"uint256"},{"name":"answeredInRound","type":"uint80"}]}
]`

// OraclePriceStrategy encodes calls for, and decodes reverts of, the rwa-v3
// OraclePriceStrategy
type OraclePriceStrategy struct{ ABI abi.ABI }

// NewOraclePriceStrategy parses the OraclePriceStrategy ABI
func NewOraclePriceStrategy() OraclePriceStrategy {
	return OraclePriceStrategy{ABI: mustABI(oraclePriceStrategyABIJSON)}
}

// Price-read error names, matching the OraclePrice.error enum in api/openapi.yaml
const (
	PriceErrStalePrice          = "StalePrice"
	PriceErrInvalidPrice        = "InvalidPrice"
	PriceErrSequencerDown       = "SequencerDown"
	PriceErrGracePeriodNotOver  = "GracePeriodNotOver"
	PriceErrFeedDecimalsChanged = "FeedDecimalsChanged"
	// PriceErrReadFailed covers RPC failures and reverts that are not a strategy error
	PriceErrReadFailed = "ReadFailed"
)

// PriceErrorName names the strategy error whose selector starts revertData, or
// PriceErrReadFailed for empty, short or unknown data
func (s OraclePriceStrategy) PriceErrorName(revertData []byte) string {
	if len(revertData) < 4 {
		return PriceErrReadFailed
	}
	for _, e := range s.ABI.Errors {
		if bytes.Equal(revertData[:4], e.ID[:4]) {
			switch e.Name {
			case PriceErrStalePrice, PriceErrInvalidPrice, PriceErrSequencerDown, PriceErrGracePeriodNotOver, PriceErrFeedDecimalsChanged:
				return e.Name
			}
		}
	}
	return PriceErrReadFailed
}

// RevertData extracts the hex revert payload that go-ethereum's JSON-RPC client attaches
// to an eth_call error (rpc.DataError), or nil when err carries none
func RevertData(err error) []byte {
	var de interface{ ErrorData() interface{} }
	if !errors.As(err, &de) {
		return nil
	}
	s, ok := de.ErrorData().(string)
	if !ok {
		return nil
	}
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	b, derr := hex.DecodeString(s)
	if derr != nil {
		return nil
	}
	return b
}

// LatestPrice is the decoded return of latestPrice()
type LatestPrice struct {
	Price     *big.Int
	UpdatedAt *big.Int
}

// UnpackLatestPrice decodes latestPrice()
func (s OraclePriceStrategy) UnpackLatestPrice(data []byte) (LatestPrice, error) {
	out, err := s.ABI.Unpack("latestPrice", data)
	if err != nil {
		return LatestPrice{}, err
	}
	return LatestPrice{
		Price:     abi.ConvertType(out[0], new(big.Int)).(*big.Int),
		UpdatedAt: abi.ConvertType(out[1], new(big.Int)).(*big.Int),
	}, nil
}

// AggregatorV3 encodes calls for the Chainlink feed reads
type AggregatorV3 struct{ ABI abi.ABI }

// NewAggregatorV3 parses the AggregatorV3Interface read ABI
func NewAggregatorV3() AggregatorV3 { return AggregatorV3{ABI: mustABI(aggregatorV3ABIJSON)} }

// UnpackUpdatedAt decodes latestRoundData() down to updatedAt
func (a AggregatorV3) UnpackUpdatedAt(data []byte) (*big.Int, error) {
	out, err := a.ABI.Unpack("latestRoundData", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[3], new(big.Int)).(*big.Int), nil
}
