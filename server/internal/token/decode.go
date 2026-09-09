// Package token decodes RWAToken's ERC-7943 (uRWA) enforcement logs into the
// JSON-friendly ChainEvent shape the project security projector folds into the
// live frozen-balance map and the latest forced-transfer summary. The token's
// other watched events are the generic OZ governance ones, which
// serverwiring's fallback still routes to internal/governance.
package token

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the RWAToken-specific events the security projector
// understands.
var EventNames = []string{"Frozen", "ForcedTransfer"}

// DecodeLog is an indexer.EventDecoder for RWAToken's ERC-7943 events. Amounts
// are returned as decimal strings, matching the string encoding the rest of
// the pipeline uses for uint256s; addresses come from the indexed topics.
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
