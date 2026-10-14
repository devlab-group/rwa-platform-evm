package bindings

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// governanceEventsABIJSON covers shared OpenZeppelin governance events:
// Pausable's Paused/Unpaused, AccessControl's RoleGranted/RoleRevoked, and
// DefaultAdminTransferScheduled/DefaultAdminTransferCanceled. Their signatures
// are shared across contracts, so one binding decodes them at any address.
//
// Paused/Unpaused put account in log.Data. RoleGranted/RoleRevoked index role,
// account and sender in log.Topics. DefaultAdminTransferScheduled indexes newAdmin
// and puts acceptSchedule in log.Data; DefaultAdminTransferCanceled has no fields.
// Acceptance emits RoleRevoked and RoleGranted, which the role projection handles,
// rather than a separate completion event.
const governanceEventsABIJSON = `[
  {"type":"event","name":"Paused","anonymous":false,"inputs":[{"name":"account","type":"address","indexed":false}]},
  {"type":"event","name":"Unpaused","anonymous":false,"inputs":[{"name":"account","type":"address","indexed":false}]},
  {"type":"event","name":"RoleGranted","anonymous":false,"inputs":[
    {"name":"role","type":"bytes32","indexed":true},{"name":"account","type":"address","indexed":true},{"name":"sender","type":"address","indexed":true}]},
  {"type":"event","name":"RoleRevoked","anonymous":false,"inputs":[
    {"name":"role","type":"bytes32","indexed":true},{"name":"account","type":"address","indexed":true},{"name":"sender","type":"address","indexed":true}]},
  {"type":"event","name":"DefaultAdminTransferScheduled","anonymous":false,"inputs":[
    {"name":"newAdmin","type":"address","indexed":true},{"name":"acceptSchedule","type":"uint48","indexed":false}]},
  {"type":"event","name":"DefaultAdminTransferCanceled","anonymous":false,"inputs":[]}
]`

// Governance decodes the generic OpenZeppelin Pausable, AccessControl and
// AccessControlDefaultAdminRules events that project contracts share
type Governance struct{ ABI abi.ABI }

// NewGovernance parses the shared governance-events ABI once.
func NewGovernance() Governance { return Governance{ABI: mustABI(governanceEventsABIJSON)} }

// EventID returns the keccak256 topic0 for the named event.
func (g Governance) EventID(name string) common.Hash { return g.ABI.Events[name].ID }

// PausedAccount decodes the non-indexed account of a Paused/Unpaused log.
func (g Governance) PausedAccount(name string, data []byte) (common.Address, error) {
	var out struct{ Account common.Address }
	if err := g.ABI.UnpackIntoInterface(&out, name, data); err != nil {
		return common.Address{}, err
	}
	return out.Account, nil
}

// RoleEvent mirrors a RoleGranted/RoleRevoked log's (all-indexed) fields.
type RoleEvent struct {
	Role    common.Hash
	Account common.Address
	Sender  common.Address
}

// UnpackRoleEvent decodes a RoleGranted/RoleRevoked log from its topics; both
// events share this layout (role, account, sender all indexed).
func (g Governance) UnpackRoleEvent(topics []common.Hash) RoleEvent {
	var ev RoleEvent
	if len(topics) >= 4 {
		ev.Role = topics[1]
		ev.Account = common.HexToAddress(topics[2].Hex())
		ev.Sender = common.HexToAddress(topics[3].Hex())
	}
	return ev
}

// AdminTransferNewAdmin decodes the indexed newAdmin (topic1) of a
// DefaultAdminTransferScheduled log, returning the zero address when the log
// has no such topic. acceptSchedule is non-indexed and not needed by the
// pending-admin projection, so it is left in log.Data undecoded.
func (g Governance) AdminTransferNewAdmin(topics []common.Hash) common.Address {
	if len(topics) >= 2 {
		return common.HexToAddress(topics[1].Hex())
	}
	return common.Address{}
}

// roleNamesByID maps each platform role's bytes32 id to its name (the set
// internal/project's knownRoles enumerates) so a decoded RoleGranted or
// RoleRevoked can be folded into the named Roles map. The security projector
// ignores an id that is not here
var roleNamesByID = map[common.Hash]string{
	common.Hash(DefaultAdminRole): "DEFAULT_ADMIN_ROLE",
	PauserRole:                    "PAUSER_ROLE",
	ComplianceRole:                "COMPLIANCE_ROLE",
	PricerRole:                    "PRICER_ROLE",
	TreasurerRole:                 "TREASURER_ROLE",
	RedemptionManagerRole:         "REDEMPTION_MANAGER_ROLE",
}

// RoleName returns the platform role name for a bytes32 role id, and false if
// the id is not one of the platform's known roles.
func RoleName(id common.Hash) (string, bool) {
	name, ok := roleNamesByID[id]
	return name, ok
}
