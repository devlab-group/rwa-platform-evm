package blockchain

import (
	"context"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// Call performs a read-only eth_call against the latest block, a small
// convenience shared by the read-model packages (internal/sales,
// internal/redemption) that pack calldata with internal/bindings and need
// nothing more than "send this, get bytes back".
func Call(ctx context.Context, client Client, to common.Address, data []byte) ([]byte, error) {
	return client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}
