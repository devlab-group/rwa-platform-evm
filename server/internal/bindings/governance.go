package bindings

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// governanceEventsABIJSON declares the generic OZ governance events every
// project contract emits but that no contract-specific binding covers:
// Pausable's Paused/Unpaused (RWAToken is ERC20Pausable), AccessControl's
// RoleGranted/RoleRevoked (every child contract inherits
// AccessControlEnumerable), and AccessControlDefaultAdminRules's
// DefaultAdminTransferScheduled/DefaultAdminTransferCanceled (the two-step
// DEFAULT_ADMIN handover every governance contract inherits). These are
// identical across every contract, so a single binding decodes them from any
// routed address — the indexer routes a log to this decoder whenever the
// address-specific decoder does not recognize its topic0. account on
// Paused/Unpaused is NON-indexed (OZ `event Paused(address account)`), so it
// lives in log.Data; RoleGranted/RoleRevoked have all three fields indexed,
// so they come entirely from log.Topics. DefaultAdminTransferScheduled's
// newAdmin is indexed (topic1) and its acceptSchedule non-indexed;
// DefaultAdminTransferCanceled carries no fields. Note there is NO dedicated
// event for a completed accept — acceptDefaultAdminTransfer moves the role via
// the standard RoleRevoked(old)+RoleGranted(new), which the role fold already
// tracks.
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

// Governance decodes the generic OZ Pausable + AccessControl events shared by
// every project contract.
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

// roleNamesByID reverse-maps each platform role's bytes32 id to its name (the
// same set knownRoles in internal/project enumerates), so a decoded
// RoleGranted/RoleRevoked can be folded into the named Roles map. An id not
// present here is a role outside the platform's model and is ignored by the
// security projector.
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
