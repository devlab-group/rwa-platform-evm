package assets

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the SupplyController events the assets reconciler
// understands.
var EventNames = []string{"Minted", "Burned", "AuditorChanged"}

// DecodeLog is an indexer.EventDecoder for SupplyController logs.
func DecodeLog(log types.Log) (string, map[string]any, error) {
	controller := bindings.NewSupplyController()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	topic0 := log.Topics[0]

	switch topic0 {
	case controller.EventID("Minted"):
		ev, err := controller.UnpackMinted(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "Minted", map[string]any{
			"recordKey": ev.RecordKey.Hex(), "metadataDigest": ev.MetadataDigest.Hex(), "vault": ev.Vault.Hex(),
			"amount": ev.Amount.String(), "nonce": ev.Nonce.String(), "auditor": ev.Auditor.Hex(),
		}, nil
	case controller.EventID("Burned"):
		ev, err := controller.UnpackBurned(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "Burned", map[string]any{
			"operationId": ev.OperationID.Hex(), "metadataDigest": ev.MetadataDigest.Hex(), "vault": ev.Vault.Hex(),
			"amount": ev.Amount.String(), "nonce": ev.Nonce.String(), "auditor": ev.Auditor.Hex(),
		}, nil
	case controller.EventID("AuditorChanged"):
		// All three fields are indexed; there is no non-indexed data to unpack.
		data := map[string]any{}
		if len(log.Topics) >= 4 {
			data["previousAuditor"] = common.HexToAddress(log.Topics[1].Hex()).Hex()
			data["newAuditor"] = common.HexToAddress(log.Topics[2].Hex()).Hex()
			data["caller"] = common.HexToAddress(log.Topics[3].Hex()).Hex()
		}
		return "AuditorChanged", data, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
