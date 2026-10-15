package bindings

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// governanceEventsABIJSON shares OpenZeppelin event layouts across contracts:
// Paused/Unpaused store account in log.Data; RoleGranted/RoleRevoked index role,
// account and sender; DefaultAdminTransferScheduled indexes newAdmin with
// acceptSchedule in log.Data; DefaultAdminTransferCanceled has no fields.
// Admin-transfer acceptance emits RoleRevoked/RoleGranted for the role projection, with
// no separate completion event.
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

// NewGovernance parses the shared governance-events ABI
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

// UnpackRoleEvent decodes the indexed role, account and sender shared by RoleGranted
// and RoleRevoked
func (g Governance) UnpackRoleEvent(topics []common.Hash) RoleEvent {
	var ev RoleEvent
	if len(topics) >= 4 {
		ev.Role = topics[1]
		ev.Account = common.HexToAddress(topics[2].Hex())
		ev.Sender = common.HexToAddress(topics[3].Hex())
	}
	return ev
}

// AdminTransferNewAdmin reads topic1 from DefaultAdminTransferScheduled, returning zero
// if absent. The pending-admin projection does not need acceptSchedule, so it remains
// undecoded in log.Data.
func (g Governance) AdminTransferNewAdmin(topics []common.Hash) common.Address {
	if len(topics) >= 2 {
		return common.HexToAddress(topics[1].Hex())
	}
	return common.Address{}
}

// roleNamesByID maps the roles in internal/project's knownRoles to projection names.
// The security projector ignores unknown IDs.
var roleNamesByID = map[common.Hash]string{
	common.Hash(DefaultAdminRole): "DEFAULT_ADMIN_ROLE",
	PauserRole:                    "PAUSER_ROLE",
	ComplianceRole:                "COMPLIANCE_ROLE",
	PricerRole:                    "PRICER_ROLE",
	TreasurerRole:                 "TREASURER_ROLE",
	RedemptionManagerRole:         "REDEMPTION_MANAGER_ROLE",
}

// RoleName returns a known platform role's name, or false for an unknown ID
func RoleName(id common.Hash) (string, bool) {
	name, ok := roleNamesByID[id]
	return name, ok
}
