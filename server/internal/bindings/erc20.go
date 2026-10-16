package bindings

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// erc20ABIJSON provides quote-token balanceOf/decimals and RWAToken
// compliance/supplyController reads for deployment wiring checks. It also declares
// symbol, totalSupply, paused and redemptionEscrow without Pack helpers.
const erc20ABIJSON = `[
  {"type":"function","name":"balanceOf","stateMutability":"view","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"decimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"function","name":"symbol","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"string"}]},
  {"type":"function","name":"totalSupply","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"paused","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"compliance","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"supplyController","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"redemptionEscrow","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"event","name":"Transfer","anonymous":false,"inputs":[
    {"name":"from","type":"address","indexed":true},{"name":"to","type":"address","indexed":true},
    {"name":"value","type":"uint256","indexed":false}]}
]`

// ERC20 encodes calldata for, and decodes results from, the ERC-20 views in erc20ABIJSON
type ERC20 struct{ ABI abi.ABI }

// NewERC20 parses the ERC-20 read-only ABI
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

// TransferEvent mirrors the ERC-20 Transfer event: sender and receiver (indexed) and
// the amount moved (non-indexed). Mints come from the zero address and burns go to it.
type TransferEvent struct {
	From  common.Address
	To    common.Address
	Value *big.Int
}

// UnpackTransfer requires exactly three topics, rejecting ERC-721 Transfer logs
func (e ERC20) UnpackTransfer(data []byte, topics []common.Hash) (TransferEvent, error) {
	var partial struct{ Value *big.Int }
	if err := e.ABI.UnpackIntoInterface(&partial, "Transfer", data); err != nil {
		return TransferEvent{}, err
	}
	if len(topics) != 3 {
		return TransferEvent{}, fmt.Errorf("bindings: Transfer log has %d topics, want 3", len(topics))
	}
	return TransferEvent{
		From:  common.HexToAddress(topics[1].Hex()),
		To:    common.HexToAddress(topics[2].Hex()),
		Value: partial.Value,
	}, nil
}
