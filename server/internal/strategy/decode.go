// Package strategy decodes FixedPriceStrategy price-update logs into the
// JSON-friendly ChainEvent shape the project security projector folds into
// the live purchase/redemption prices. The strategy contract is not one of
// the four originally-indexed addresses (compliance/supply-controller/vault/
// escrow); it becomes reachable once the indexer's watch set is derived from
// the deployed Project record (serverwiring.IndexerAddresses).
package strategy

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the FixedPriceStrategy events the security projector
// understands.
var EventNames = []string{"PurchasePriceUpdated", "RedemptionPriceUpdated"}

// DecodeLog is an indexer.EventDecoder for FixedPriceStrategy logs: it
// recognizes the two price-update events and returns their previous/new
// prices as decimal strings (matching the string encoding the rest of the
// pipeline uses for uint256s) plus the indexed caller.
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
