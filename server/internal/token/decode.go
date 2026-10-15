// Package token decodes RWAToken ERC-7943 logs into JSON-friendly ChainEvent fields for
// project security's frozen-balance map and latest forced-transfer summary.
// serverwiring sends other watched token events to internal/governance.
package token

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames lists RWAToken-specific events consumed by project security
var EventNames = []string{"Frozen", "ForcedTransfer"}

// DecodeLog is an indexer.EventDecoder for RWAToken ERC-7943 events, with
// decimal-string amounts and addresses from indexed topics
func DecodeLog(log types.Log) (string, map[string]any, error) {
	t := bindings.NewRWAToken()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}

	switch log.Topics[0] {
	case t.EventID("Frozen"):
		ev, err := t.UnpackFrozen(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "Frozen", map[string]any{"account": ev.Account.Hex(), "amount": ev.Amount.String()}, nil
	case t.EventID("ForcedTransfer"):
		ev, err := t.UnpackForcedTransfer(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "ForcedTransfer", map[string]any{
			"from": ev.From.Hex(), "to": ev.To.Hex(), "amount": ev.Amount.String(),
		}, nil
	default:
		return "unknown", map[string]any{"topic0": log.Topics[0].Hex()}, nil
	}
}
