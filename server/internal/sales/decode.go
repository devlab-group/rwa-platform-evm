package sales

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the Vault events decoded from Vault logs: Purchased (the
// sales read model), TreasuryChanged and StrategyChanged (the governance-
// authority changes the project security projector folds), and
// ProceedsWithdrawn (a treasury withdrawal, surfaced by the stack-transactions
// projector). The off-chain distribution feature (Vault.Distributed) has been
// removed from the platform — only on-chain purchases remain.
var EventNames = []string{"Purchased", "TreasuryChanged", "StrategyChanged", "ProceedsWithdrawn"}

// DecodeLog is an indexer.EventDecoder for Vault logs: it recognizes topic0
// against Purchased (the sales read model), TreasuryChanged and
// StrategyChanged (the security projector's live treasury/strategy), and
// ProceedsWithdrawn (treasury withdrawals), and returns JSON-friendly fields,
// the same convention as internal/redemption.DecodeLog. Any other Vault log
// falls through to the generic "unknown" bucket (serverwiring then tries the
// shared governance decoder for RoleGranted/RoleRevoked).
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
			"buyer": ev.Buyer.Hex(), "recipient": ev.Recipient.Hex(),
			"tokenAmount": ev.TokenAmount.String(), "quoteAmount": ev.QuoteAmount.String(),
			"strategy": ev.Strategy.Hex(),
		}, nil
	case vault.EventID("TreasuryChanged"):
		ev := vault.UnpackTreasuryChanged(log.Topics)
		return "TreasuryChanged", map[string]any{
			"previousTreasury": ev.Previous.Hex(), "newTreasury": ev.New.Hex(), "caller": ev.Caller.Hex(),
		}, nil
	case vault.EventID("StrategyChanged"):
		ev := vault.UnpackStrategyChanged(log.Topics)
		return "StrategyChanged", map[string]any{
			"previousStrategy": ev.Previous.Hex(), "newStrategy": ev.New.Hex(), "caller": ev.Caller.Hex(),
		}, nil
	case vault.EventID("ProceedsWithdrawn"):
		ev, err := vault.UnpackProceedsWithdrawn(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "ProceedsWithdrawn", map[string]any{
			"treasury": ev.Treasury.Hex(), "quoteAmount": ev.QuoteAmount.String(), "caller": ev.Caller.Hex(),
		}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
