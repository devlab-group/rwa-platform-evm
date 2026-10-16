// Package token decodes RWAToken ERC-20 Transfer and ERC-7943 logs into JSON-friendly ChainEvent fields for
// project security's frozen-balance map and latest forced-transfer summary.
// serverwiring sends other watched token events to internal/governance.
package token

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames lists RWAToken events consumed by project security and income snapshots
var EventNames = []string{"Frozen", "ForcedTransfer", "Transfer"}

// Parsed once: DecodeLog runs for every token Transfer the indexer ingests
var (
	rwaToken = bindings.NewRWAToken()
	erc20    = bindings.NewERC20()
)

// DecodeLog is an indexer.EventDecoder for RWAToken Transfer and ERC-7943 events, with
// decimal-string amounts and addresses from indexed topics
func DecodeLog(log types.Log) (string, map[string]any, error) {
	t := rwaToken
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}

	switch log.Topics[0] {
	case erc20.ABI.Events["Transfer"].ID:
		ev, err := erc20.UnpackTransfer(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "Transfer", map[string]any{"from": ev.From.Hex(), "to": ev.To.Hex(), "value": ev.Value.String()}, nil
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
