package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// erc20ABIJSON declares the read methods the server calls on ERC-20 contracts:
// balanceOf and decimals for the quote token, and RWAToken's compliance() and
// supplyController() getters, which deployment verification uses to confirm that
// the token is wired to the project's own compliance registry and supply
// controller. It also declares symbol, totalSupply, paused and redemptionEscrow,
// which have no Pack helpers here.
const erc20ABIJSON = `[
  {"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"decimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"function","name":"symbol","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"string"}]},
  {"type":"function","name":"totalSupply","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"paused","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"compliance","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"supplyController","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"redemptionEscrow","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]}
]`

// ERC20 encodes calldata for, and decodes results from, the ERC-20 views in erc20ABIJSON
type ERC20 struct{ ABI abi.ABI }

// NewERC20 parses the ERC-20 read-only ABI once.
func NewERC20() ERC20 { return ERC20{ABI: mustABI(erc20ABIJSON)} }

// PackBalanceOf builds calldata for balanceOf(address).
func (e ERC20) PackBalanceOf(account common.Address) ([]byte, error) {
	return e.ABI.Pack("balanceOf", account)
}

// UnpackBalanceOf decodes the return value of balanceOf.
func (e ERC20) UnpackBalanceOf(data []byte) (*big.Int, error) {
	out, err := e.ABI.Unpack("balanceOf", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PackDecimals builds calldata for decimals().
func (e ERC20) PackDecimals() ([]byte, error) { return e.ABI.Pack("decimals") }

// UnpackDecimals decodes the return value of decimals().
func (e ERC20) UnpackDecimals(data []byte) (uint8, error) {
	out, err := e.ABI.Unpack("decimals", data)
	if err != nil {
		return 0, err
	}
	return *abi.ConvertType(out[0], new(uint8)).(*uint8), nil
}
