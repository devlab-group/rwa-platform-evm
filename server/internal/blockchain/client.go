// Package blockchain wraps the go-ethereum JSON-RPC client and implements a
// per-key transaction manager. Callers depend on the Client and TxManager
// interfaces, so they can be tested with the fakes in this package without a
// live chain.
package blockchain

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Client is the subset of go-ethereum RPC calls the server needs. It is
// satisfied by *RPCClient (wrapping ethclient.Client) and by *FakeClient in
// tests.
type Client interface {
	ChainID(ctx context.Context) (*big.Int, error)
	BlockNumber(ctx context.Context) (uint64, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	// NonceAt returns the account's nonce as of the given block; nil means
	// "latest", as in eth_getTransactionCount(address, "latest"). That is the
	// count of mined transactions, unlike PendingNonceAt, which also counts the
	// node's pending transactions. TxManager compares both with its own records
	// before it allocates a nonce.
	NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error)
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	// TransactionByHash returns the mined or pending transaction (isPending
	// reports which) and ethereum.NotFound when the hash is unknown. Post-deploy
	// verification uses it to decode the input calldata of the observed
	// RWAFactory.deploy transaction, which holds the ProjectConfig that the
	// ProjectDeployed event does not carry.
	TransactionByHash(ctx context.Context, txHash common.Hash) (tx *types.Transaction, isPending bool, err error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	// CodeAt returns the deployed bytecode at address (a nil blockNumber means
	// latest). Post-deploy verification uses it: a non-empty result shows that
	// some contract exists at the address.
	CodeAt(ctx context.Context, address common.Address, blockNumber *big.Int) ([]byte, error)
}

// RPCClient adapts *ethclient.Client to the Client interface.
type RPCClient struct {
	c *ethclient.Client
}

// Dial connects to an EVM JSON-RPC endpoint.
func Dial(ctx context.Context, url string) (*RPCClient, error) {
	c, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return nil, err
	}
	return &RPCClient{c: c}, nil
}

func (r *RPCClient) ChainID(ctx context.Context) (*big.Int, error)   { return r.c.ChainID(ctx) }
func (r *RPCClient) BlockNumber(ctx context.Context) (uint64, error) { return r.c.BlockNumber(ctx) }
func (r *RPCClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return r.c.HeaderByNumber(ctx, number)
}
func (r *RPCClient) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	return r.c.PendingNonceAt(ctx, account)
}
func (r *RPCClient) NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error) {
	return r.c.NonceAt(ctx, account, blockNumber)
}
func (r *RPCClient) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	return r.c.SuggestGasTipCap(ctx)
}
func (r *RPCClient) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return r.c.SuggestGasPrice(ctx)
}
func (r *RPCClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return r.c.EstimateGas(ctx, msg)
}
func (r *RPCClient) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	return r.c.CallContract(ctx, msg, blockNumber)
}
func (r *RPCClient) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	return r.c.SendTransaction(ctx, tx)
}
func (r *RPCClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	return r.c.TransactionReceipt(ctx, txHash)
}
func (r *RPCClient) TransactionByHash(ctx context.Context, txHash common.Hash) (*types.Transaction, bool, error) {
	return r.c.TransactionByHash(ctx, txHash)
}
func (r *RPCClient) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	return r.c.FilterLogs(ctx, q)
}
func (r *RPCClient) CodeAt(ctx context.Context, address common.Address, blockNumber *big.Int) ([]byte, error) {
	return r.c.CodeAt(ctx, address, blockNumber)
}

// Close releases the underlying RPC connection.
func (r *RPCClient) Close() { r.c.Close() }
