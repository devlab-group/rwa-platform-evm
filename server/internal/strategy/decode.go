// Package strategy decodes FixedPriceStrategy price updates into JSON-friendly
// ChainEvent fields for project security's live purchase/redemption prices.
// cmd/platform follows Security.Strategy in the indexer watch set via
// serverwiring.IndexerAddresses, including after strategy swaps.
package strategy

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames lists FixedPriceStrategy events consumed by project security
var EventNames = []string{"PurchasePriceUpdated", "RedemptionPriceUpdated"}

// DecodeLog is an indexer.EventDecoder for both price updates, returning previous/new
// uint256 prices as decimal strings and the indexed caller
func DecodeLog(log types.Log) (string, map[string]any, error) {
	s := bindings.NewFixedPriceStrategy()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	topic0 := log.Topics[0]

	switch topic0 {
	case s.EventID("PurchasePriceUpdated"):
		ev, err := s.UnpackPriceUpdated("PurchasePriceUpdated", log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "PurchasePriceUpdated", map[string]any{
			"previousPrice": ev.PreviousPrice.String(), "newPrice": ev.NewPrice.String(), "caller": ev.Caller.Hex(),
		}, nil
	case s.EventID("RedemptionPriceUpdated"):
		ev, err := s.UnpackPriceUpdated("RedemptionPriceUpdated", log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "RedemptionPriceUpdated", map[string]any{
			"previousPrice": ev.PreviousPrice.String(), "newPrice": ev.NewPrice.String(), "caller": ev.Caller.Hex(),
		}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
