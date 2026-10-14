package redemption

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the RedemptionEscrow events the projector understands, in the
// order IRedemptionEscrow declares them
var EventNames = []string{
	"RedemptionRequested", "RedemptionFunded", "RedemptionCompleted", "RedemptionRejected", "RedemptionCancelled",
}

// DecodeLog is an indexer.EventDecoder for RedemptionEscrow logs. It decodes the
// five redemption events into fields that fit models.ChainEvent.Data: decimal
// strings for numbers and 0x-hex strings for addresses and hashes, except
// createdAt, which keeps its numeric type. Any other log is reported as
// "unknown", and serverwiring then tries the shared governance decoder.
func DecodeLog(log types.Log) (string, map[string]any, error) {
	escrow := bindings.NewRedemptionEscrow()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	topic0 := log.Topics[0]

	switch topic0 {
	case escrow.EventID("RedemptionRequested"):
		ev, err := escrow.UnpackRedemptionRequested(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "RedemptionRequested", map[string]any{
			"id": ev.ID.String(), "beneficiary": ev.Beneficiary.Hex(),
			"rwaAmount": ev.RWAAmount.String(), "quoteAmount": ev.QuoteAmount.String(),
			"createdAt": ev.CreatedAt,
		}, nil
	case escrow.EventID("RedemptionFunded"):
		ev, err := escrow.UnpackRedemptionFunded(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "RedemptionFunded", map[string]any{
			"id": ev.ID.String(), "funder": ev.Funder.Hex(), "quoteAmount": ev.QuoteAmount.String(),
		}, nil
	case escrow.EventID("RedemptionCompleted"):
		ev, err := escrow.UnpackRedemptionCompleted(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "RedemptionCompleted", map[string]any{
			"id": ev.ID.String(), "beneficiary": ev.Beneficiary.Hex(),
			"rwaAmount": ev.RWAAmount.String(), "quoteAmount": ev.QuoteAmount.String(),
		}, nil
	case escrow.EventID("RedemptionRejected"):
		ev := escrow.UnpackRedemptionRejected(log.Topics)
		return "RedemptionRejected", map[string]any{
			"id": ev.ID.String(), "reasonCode": ev.ReasonCode.Hex(), "caller": ev.Caller.Hex(),
		}, nil
	case escrow.EventID("RedemptionCancelled"):
		ev := escrow.UnpackRedemptionCancelled(log.Topics)
		return "RedemptionCancelled", map[string]any{
			"id": ev.ID.String(), "beneficiary": ev.Beneficiary.Hex(),
		}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
