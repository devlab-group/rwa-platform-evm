// Package governance decodes the generic OpenZeppelin governance events every
// project contract can emit — Pausable's Paused/Unpaused, AccessControl's
// RoleGranted/RoleRevoked, and AccessControlDefaultAdminRules's
// DefaultAdminTransferScheduled/DefaultAdminTransferCanceled — which no
// contract-specific decoder covers. Because
// these have identical signatures on every contract, serverwiring routes any
// log a contract-specific decoder returns as "unknown" to this decoder as a
// fallback (and routes the Token, which has no other watched events, here
// directly). The project security projector folds the resulting ChainEvents
// into the live paused flag and per-contract role holder sets.
package governance

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames are the generic governance events this package decodes on any
// project contract.
var EventNames = []string{
	"Paused", "Unpaused", "RoleGranted", "RoleRevoked",
	"DefaultAdminTransferScheduled", "DefaultAdminTransferCanceled",
}

// DecodeLog is an indexer.EventDecoder for the shared OZ governance events.
// Paused/Unpaused carry only the non-indexed acting account; RoleGranted/
// RoleRevoked carry role, account, and sender (all indexed). role is emitted
// as its raw bytes32 hex — the projector maps it to a role name via
// bindings.RoleName.
func DecodeLog(log types.Log) (string, map[string]any, error) {
	g := bindings.NewGovernance()
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	topic0 := log.Topics[0]

	switch topic0 {
	case g.EventID("Paused"), g.EventID("Unpaused"):
		name := "Paused"
		if topic0 == g.EventID("Unpaused") {
			name = "Unpaused"
		}
		account, err := g.PausedAccount(name, log.Data)
		if err != nil {
			return "", nil, err
		}
		return name, map[string]any{"account": account.Hex()}, nil
	case g.EventID("RoleGranted"), g.EventID("RoleRevoked"):
		name := "RoleGranted"
		if topic0 == g.EventID("RoleRevoked") {
			name = "RoleRevoked"
		}
		ev := g.UnpackRoleEvent(log.Topics)
		return name, map[string]any{
			"role": ev.Role.Hex(), "account": ev.Account.Hex(), "sender": ev.Sender.Hex(),
		}, nil
	case g.EventID("DefaultAdminTransferScheduled"):
		// newAdmin is the only field the pending-admin projector needs; the
		// non-indexed acceptSchedule is left undecoded.
		return "DefaultAdminTransferScheduled", map[string]any{
			"newAdmin": g.AdminTransferNewAdmin(log.Topics).Hex(),
		}, nil
	case g.EventID("DefaultAdminTransferCanceled"):
		return "DefaultAdminTransferCanceled", map[string]any{}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
