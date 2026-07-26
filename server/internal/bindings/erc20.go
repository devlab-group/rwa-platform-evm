package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// erc20ABIJSON covers the read methods the server needs: quote-token
// balance display and decimals for formatting, plus IRWAToken's own
// compliance()/supplyController()/paused() getters, used to
// verify a deployed RWAToken is actually wired to the SAME compliance
// registry and supply controller the rest of verify.go checked, not just
// that all six addresses independently look plausible. mint/burn are never
// invoked by the server (only SupplyController may call them).
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

// ERC20 encodes calldata / decodes reads for the standard ERC-20 view surface.
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
