package blockchain

import (
	"context"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// Call runs eth_call at the latest block for read models such as sales and redemption,
// using bindings-packed calldata
func Call(ctx context.Context, client Client, to common.Address, data []byte) ([]byte, error) {
	return client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}
