package blockchain

import (
	"context"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// Call runs eth_call at the latest block for read models such as sales and redemption,
// using bindings-packed calldata
func Call(ctx context.Context, client Client, to common.Address, data []byte) ([]byte, error) {
	return client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}

// IsRevert recognizes errors carrying ErrorData and either RPC code 3 or an
// "execution reverted" message
func IsRevert(err error) bool {
	var de interface{ ErrorData() interface{} }
	if !errors.As(err, &de) {
		return false
	}
	var ce interface{ ErrorCode() int }
	if errors.As(err, &ce) && ce.ErrorCode() == 3 {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "execution reverted")
}
