package sales

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames lists Vault events: Purchased feeds sales, TreasuryChanged/StrategyChanged
// feed project security, MinPurchaseQuoteChanged (rwa-v3) feeds the projected minimum,
// ProceedsWithdrawn feeds txindex's treasury-withdrawal history and the five Purchase*
// order events (rwa-v4) feed the order read model
var EventNames = append([]string{"Purchased", "TreasuryChanged", "StrategyChanged", "ProceedsWithdrawn", "MinPurchaseQuoteChanged"}, OrderEventNames...)

// OrderEventNames are the rwa-v4 forward-order events the order projector replays
var OrderEventNames = []string{"PurchaseRequested", "PurchaseSettled", "PurchaseRefunded", "PurchaseCancelled", "PurchaseRejected"}

// DecodeLog is an indexer.EventDecoder for Vault EventNames, using JSON-friendly fields
// as in internal/redemption.DecodeLog; other logs return "unknown" for serverwiring's
// governance fallback
func DecodeLog(log types.Log) (string, map[string]any, error) {
	vault := bindings.NewVault()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	topic0 := log.Topics[0]

	switch topic0 {
	case vault.EventID("Purchased"):
		ev, err := vault.UnpackPurchased(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "Purchased", map[string]any{
			"buyer":       ev.Buyer.Hex(),
			"recipient":   ev.Recipient.Hex(),
			"tokenAmount": ev.TokenAmount.String(),
			"quoteAmount": ev.QuoteAmount.String(),
			"strategy":    ev.Strategy.Hex(),
		}, nil
	case vault.EventID("TreasuryChanged"):
		ev := vault.UnpackTreasuryChanged(log.Topics)
		return "TreasuryChanged", map[string]any{
			"previousTreasury": ev.Previous.Hex(),
			"newTreasury":      ev.New.Hex(),
			"caller":           ev.Caller.Hex(),
		}, nil
	case vault.EventID("StrategyChanged"):
		ev := vault.UnpackStrategyChanged(log.Topics)
		return "StrategyChanged", map[string]any{
			"previousStrategy": ev.Previous.Hex(),
			"newStrategy":      ev.New.Hex(),
			"caller":           ev.Caller.Hex(),
		}, nil
	case vault.EventID("ProceedsWithdrawn"):
		ev, err := vault.UnpackProceedsWithdrawn(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "ProceedsWithdrawn", map[string]any{
			"treasury":    ev.Treasury.Hex(),
			"quoteAmount": ev.QuoteAmount.String(),
			"caller":      ev.Caller.Hex(),
		}, nil
	case vault.EventID("MinPurchaseQuoteChanged"):
		ev, err := vault.UnpackMinPurchaseQuoteChanged(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "MinPurchaseQuoteChanged", map[string]any{
			"previousMinimum": ev.PreviousMinimum.String(),
			"newMinimum":      ev.NewMinimum.String(),
			"caller":          ev.Caller.Hex(),
		}, nil
	case vault.EventID("PurchaseRequested"):
		ev, err := vault.UnpackPurchaseRequested(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		// baseRound is a uint80, so it is a decimal string like the amounts; createdAt stays numeric
		return "PurchaseRequested", map[string]any{
			"id":             ev.ID.String(),
			"buyer":          ev.Buyer.Hex(),
			"recipient":      ev.Recipient.Hex(),
			"tokenAmount":    ev.TokenAmount.String(),
			"maxQuoteAmount": ev.MaxQuoteAmount.String(),
			"strategy":       ev.Strategy.Hex(),
			"baseRound":      ev.BaseRound.String(),
			"createdAt":      ev.CreatedAt,
		}, nil
	case vault.EventID("PurchaseSettled"), vault.EventID("PurchaseRefunded"):
		name := "PurchaseSettled"
		if topic0 == vault.EventID("PurchaseRefunded") {
			name = "PurchaseRefunded"
		}
		ev, err := vault.UnpackPurchasePriced(name, log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return name, map[string]any{
			"id":          ev.ID.String(),
			"roundId":     ev.RoundID.String(),
			"quoteAmount": ev.QuoteAmount.String(),
			"refund":      ev.Refund.String(),
		}, nil
	case vault.EventID("PurchaseCancelled"), vault.EventID("PurchaseRejected"):
		name := "PurchaseCancelled"
		if topic0 == vault.EventID("PurchaseRejected") {
			name = "PurchaseRejected"
		}
		ev, err := vault.UnpackPurchaseClosed(name, log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return name, map[string]any{"id": ev.ID.String(), "buyer": ev.Buyer.Hex(), "refund": ev.Refund.String()}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
