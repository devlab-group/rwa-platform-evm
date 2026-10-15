package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const accessControlABIJSON = `[
  {"type":"function","name":"hasRole","stateMutability":"view","inputs":[
    {"name":"role","type":"bytes32"},{"name":"account","type":"address"}],
    "outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"getRoleAdmin","stateMutability":"view","inputs":[{"name":"role","type":"bytes32"}],
    "outputs":[{"name":"","type":"bytes32"}]},
  {"type":"function","name":"getRoleMemberCount","stateMutability":"view","inputs":[{"name":"role","type":"bytes32"}],
    "outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"getRoleMember","stateMutability":"view","inputs":[
    {"name":"role","type":"bytes32"},{"name":"index","type":"uint256"}],
    "outputs":[{"name":"","type":"address"}]}
]`

// AccessControl binds hasRole, getRoleAdmin, getRoleMemberCount and getRoleMember
// across the platform's AccessControlEnumerable governance contracts
// (docs/spec/roles.md). Deployment verification enumerates role holders to detect
// unexpected members that checking known candidates with hasRole would miss.
type AccessControl struct{ ABI abi.ABI }

// NewAccessControl parses the shared AccessControl ABI
func NewAccessControl() AccessControl { return AccessControl{ABI: mustABI(accessControlABIJSON)} }

// PackHasRole builds calldata for hasRole(bytes32,address).
func (a AccessControl) PackHasRole(role [32]byte, account common.Address) ([]byte, error) {
	return a.ABI.Pack("hasRole", role, account)
}

// UnpackHasRole decodes the return value of hasRole.
func (a AccessControl) UnpackHasRole(data []byte) (bool, error) {
	out, err := a.ABI.Unpack("hasRole", data)
	if err != nil {
		return false, err
	}
	return *abi.ConvertType(out[0], new(bool)).(*bool), nil
}

// PackGetRoleMemberCount builds calldata for getRoleMemberCount(bytes32).
func (a AccessControl) PackGetRoleMemberCount(role [32]byte) ([]byte, error) {
	return a.ABI.Pack("getRoleMemberCount", role)
}

// UnpackGetRoleMemberCount decodes the uint256 return of getRoleMemberCount.
func (a AccessControl) UnpackGetRoleMemberCount(data []byte) (*big.Int, error) {
	out, err := a.ABI.Unpack("getRoleMemberCount", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PackGetRoleMember builds calldata for getRoleMember(bytes32,uint256).
func (a AccessControl) PackGetRoleMember(role [32]byte, index *big.Int) ([]byte, error) {
	return a.ABI.Pack("getRoleMember", role, index)
}

// UnpackGetRoleMember decodes the address return of getRoleMember.
func (a AccessControl) UnpackGetRoleMember(data []byte) (common.Address, error) {
	out, err := a.ABI.Unpack("getRoleMember", data)
	if err != nil {
		return common.Address{}, err
	}
	return *abi.ConvertType(out[0], new(common.Address)).(*common.Address), nil
}

// Role IDs as defined in docs/spec/roles.md. DefaultAdminRole is OpenZeppelin's
// bytes32(0); the others are the keccak256 of the role name.
var (
	DefaultAdminRole      = [32]byte{}
	PauserRole            = crypto.Keccak256Hash([]byte("PAUSER_ROLE"))
	ComplianceRole        = crypto.Keccak256Hash([]byte("COMPLIANCE_ROLE"))
	PricerRole            = crypto.Keccak256Hash([]byte("PRICER_ROLE"))
	TreasurerRole         = crypto.Keccak256Hash([]byte("TREASURER_ROLE"))
	RedemptionManagerRole = crypto.Keccak256Hash([]byte("REDEMPTION_MANAGER_ROLE"))
)
