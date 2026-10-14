// Package strategy decodes FixedPriceStrategy price-update logs into the
// JSON-friendly ChainEvent shape that the project security projector folds into
// the live purchase and redemption prices. The indexer watches the strategy
// address because its watch set comes from the deployed Project record
// (serverwiring.IndexerAddresses).
package strategy

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the FixedPriceStrategy events the security projector
// understands.
var EventNames = []string{"PurchasePriceUpdated", "RedemptionPriceUpdated"}

// DecodeLog is an indexer.EventDecoder for FixedPriceStrategy logs. It decodes
// the two price-update events into the previous and new price, as decimal
// strings like every uint256 in the pipeline, plus the indexed caller.
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
