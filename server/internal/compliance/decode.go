package compliance

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// DecodeLog is an indexer.EventDecoder for ComplianceRegistry logs.
func DecodeLog(log types.Log) (string, map[string]any, error) {
	registry := bindings.NewComplianceRegistry()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	if log.Topics[0] != registry.EventID("StatusChanged") {
		return "unknown", map[string]any{"topic0": log.Topics[0].Hex()}, nil
	}
	ev, err := registry.UnpackStatusChanged(log.Data, log.Topics)
	if err != nil {
		return "", nil, err
	}
	return "StatusChanged", map[string]any{
		"account": ev.Account.Hex(), "previousStatus": ev.PreviousStatus, "newStatus": ev.NewStatus,
		"previousValidUntil": ev.PreviousValidUntil, "newValidUntil": ev.NewValidUntil, "caller": ev.Caller.Hex(),
	}, nil
}
