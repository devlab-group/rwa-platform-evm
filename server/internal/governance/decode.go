// Package governance decodes the generic OpenZeppelin events that project
// contracts share: Pausable's Paused/Unpaused (emitted only by RWAToken),
// AccessControl's RoleGranted/RoleRevoked, and
// AccessControlDefaultAdminRules's DefaultAdminTransferScheduled/
// DefaultAdminTransferCanceled. The signatures are identical on every contract,
// so serverwiring passes any log that a contract-specific decoder reports as
// "unknown" to this decoder. The project security projector folds the resulting
// ChainEvents into the live paused flag, the role holder sets and the pending
// admin.
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
// Paused/Unpaused carry only the non-indexed account that paused or unpaused;
// RoleGranted/RoleRevoked carry role, account and sender, all indexed. role is
// returned as its raw bytes32 hex, which the projector maps to a role name with
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
		// the pending-admin projection needs only newAdmin, so the non-indexed
		// acceptSchedule stays undecoded
		return "DefaultAdminTransferScheduled", map[string]any{
			"newAdmin": g.AdminTransferNewAdmin(log.Topics).Hex(),
		}, nil
	case g.EventID("DefaultAdminTransferCanceled"):
		return "DefaultAdminTransferCanceled", map[string]any{}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
