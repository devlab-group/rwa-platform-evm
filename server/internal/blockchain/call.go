package blockchain

import (
	"context"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// Call performs a read-only eth_call against the latest block. The read-model
// packages (internal/sales, internal/redemption) use it with calldata packed by
// internal/bindings
func Call(ctx context.Context, client Client, to common.Address, data []byte) ([]byte, error) {
	return client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}
