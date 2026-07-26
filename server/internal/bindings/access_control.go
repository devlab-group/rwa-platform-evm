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

// AccessControl encodes calldata / decodes reads for OZ AccessControl's
// standard surface (hasRole/getRoleAdmin) plus the AccessControlEnumerable
// extension (getRoleMemberCount/getRoleMember), shared verbatim by every
// project contract that grants roles (docs/spec/roles.md: "Each contract may
// host its own AccessControl instance ... implementer choice"). One binding
// works against any of them since the function signatures are identical. The
// enumerable calls let deployment verification enumerate the COMPLETE holder
// set of every role and reject an unexpected holder, which a candidate-only
// hasRole check cannot detect; every child contract inherits
// AccessControlEnumerable, so these calls are always present.
type AccessControl struct{ ABI abi.ABI }

// NewAccessControl parses the shared AccessControl ABI once.
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

// Role IDs per docs/spec/roles.md ("FROZEN — lead-owned"). DefaultAdminRole
// is OZ's fixed bytes32(0); the rest are keccak256 of the exact role name string.
var (
	DefaultAdminRole      = [32]byte{}
	PauserRole            = crypto.Keccak256Hash([]byte("PAUSER_ROLE"))
	ComplianceRole        = crypto.Keccak256Hash([]byte("COMPLIANCE_ROLE"))
	PricerRole            = crypto.Keccak256Hash([]byte("PRICER_ROLE"))
	TreasurerRole         = crypto.Keccak256Hash([]byte("TREASURER_ROLE"))
	RedemptionManagerRole = crypto.Keccak256Hash([]byte("REDEMPTION_MANAGER_ROLE"))
)
