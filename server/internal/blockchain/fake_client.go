package blockchain

import (
	"context"
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// FakeClient supplies canned in-memory RPC responses and records SentTxs for tests,
// without network I/O
type FakeClient struct {
	mu sync.Mutex

	ChainIDValue *big.Int
	BlockNum     uint64
	// BaseFee controls synthesized headers; nil makes TxManager fall back to legacy
	// transactions
	BaseFee *big.Int
	// Headers overrides synthesized headers by block number. Pin a header's Hash() or use
	// nil for ethereum.NotFound to test receipt/header mismatches and disappearing blocks
	// during reorgs.
	Headers map[uint64]*types.Header
	// Nonces supplies PendingNonceAt, equivalent to eth_getTransactionCount(...,
	// "pending")
	Nonces map[common.Address]uint64
	// MinedNonces supplies NonceAt, independently of pending Nonces, and defaults to zero.
	// Divergent values simulate stuck pending transactions or externally consumed nonces
	// for reconciliation tests.
	MinedNonces   map[common.Address]uint64
	GasTipCap     *big.Int
	GasPrice      *big.Int
	EstimatedGas  uint64
	CallResponses map[string][]byte // keyed by hex(msg.Data)
	// Calls records CallContract arguments in order, distinguishing destination contracts
	// that calldata-only CallResponses cannot
	Calls []ethereum.CallMsg
	// DefaultCallResponse supplies unmatched calldata instead of an error, letting batches
	// of hasRole or isAllowed calls share a response
	DefaultCallResponse []byte
	Receipts            map[common.Hash]*types.Receipt
	// Txs holds canned TransactionByHash responses keyed by hash; an absent hash
	// returns ethereum.NotFound, as a node does
	Txs  map[common.Hash]*types.Transaction
	Logs []types.Log
	// Code supplies CodeAt by address; absent entries return empty bytecode
	Code map[common.Address][]byte

	SentTxs []*types.Transaction

	// SendTransactionErr, if set, is returned by every SendTransaction call.
	SendTransactionErr error

	// AutoIncrementNonce updates Nonces[sender] to tx.Nonce()+1 during SendTransaction. It
	// defaults to false; tests then use SetNonce.
	// Updating at broadcast keeps concurrency tests dependent on TxManager's per-key lock.
	// Incrementing during PendingNonceAt would serialize allocation inside the fake and
	// hide a missing manager lock.
	AutoIncrementNonce bool
}

// NewFakeClient returns a FakeClient with reasonable local-devnet defaults.
func NewFakeClient() *FakeClient {
	return &FakeClient{
		ChainIDValue:  big.NewInt(31337),
		BlockNum:      100,
		BaseFee:       big.NewInt(1_000_000_000),
		Nonces:        map[common.Address]uint64{},
		MinedNonces:   map[common.Address]uint64{},
		GasTipCap:     big.NewInt(1_000_000_000),
		GasPrice:      big.NewInt(2_000_000_000),
		EstimatedGas:  100000,
		CallResponses: map[string][]byte{},
		Receipts:      map[common.Hash]*types.Receipt{},
		Txs:           map[common.Hash]*types.Transaction{},
		Code:          map[common.Address][]byte{},
	}
}

func (f *FakeClient) ChainID(ctx context.Context) (*big.Int, error) { return f.ChainIDValue, nil }

func (f *FakeClient) BlockNumber(ctx context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.BlockNum, nil
}

func (f *FakeClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.BlockNum
	if number != nil {
		n = number.Uint64()
	}
	if h, ok := f.Headers[n]; ok {
		if h == nil {
			return nil, ethereum.NotFound // simulates the height no longer resolving (e.g. a deep reorg)
		}
		return h, nil
	}
	return &types.Header{Number: new(big.Int).SetUint64(n), BaseFee: f.BaseFee}, nil
}

// SetHeader overrides a block's synthesized header, controlling its hash for reorg
// tests. A nil header makes the height return ethereum.NotFound.
func (f *FakeClient) SetHeader(number uint64, h *types.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Headers == nil {
		f.Headers = map[uint64]*types.Header{}
	}
	f.Headers[number] = h
}

func (f *FakeClient) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Nonces[account], nil
}

// SetNonce seeds the pending nonce for an account
func (f *FakeClient) SetNonce(account common.Address, nonce uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Nonces[account] = nonce
}

func (f *FakeClient) NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.MinedNonces[account], nil
}

// SetMinedNonce seeds the latest (mined) nonce for an account, independent of
// the pending nonce set with SetNonce (see MinedNonces)
func (f *FakeClient) SetMinedNonce(account common.Address, nonce uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MinedNonces == nil {
		f.MinedNonces = map[common.Address]uint64{}
	}
	f.MinedNonces[account] = nonce
}

func (f *FakeClient) SuggestGasTipCap(ctx context.Context) (*big.Int, error) { return f.GasTipCap, nil }
func (f *FakeClient) SuggestGasPrice(ctx context.Context) (*big.Int, error)  { return f.GasPrice, nil }
func (f *FakeClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return f.EstimatedGas, nil
}

func (f *FakeClient) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, msg)
	key := common.Bytes2Hex(msg.Data)
	if resp, ok := f.CallResponses[key]; ok {
		return resp, nil
	}
	if f.DefaultCallResponse != nil {
		return f.DefaultCallResponse, nil
	}
	return nil, errors.New("blockchain: fake client has no canned response for this call")
}

func (f *FakeClient) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendTransactionErr != nil {
		return f.SendTransactionErr
	}
	f.SentTxs = append(f.SentTxs, tx)
	if f.AutoIncrementNonce {
		if sender, err := types.Sender(types.LatestSignerForChainID(f.ChainIDValue), tx); err == nil {
			if next := tx.Nonce() + 1; next > f.Nonces[sender] {
				f.Nonces[sender] = next
			}
		}
	}
	return nil
}

func (f *FakeClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.Receipts[txHash]
	if !ok {
		return nil, ethereum.NotFound
	}
	return r, nil
}

// SetReceipt marks a transaction as mined with the given status and block
func (f *FakeClient) SetReceipt(txHash common.Hash, r *types.Receipt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Receipts[txHash] = r
}

func (f *FakeClient) TransactionByHash(ctx context.Context, txHash common.Hash) (*types.Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tx, ok := f.Txs[txHash]
	if !ok {
		return nil, false, ethereum.NotFound
	}
	return tx, false, nil
}

// SetTx registers a transaction for TransactionByHash, for example the observed
// RWAFactory.deploy calldata that post-deploy verification reads
func (f *FakeClient) SetTx(txHash common.Hash, tx *types.Transaction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Txs[txHash] = tx
}

// RemoveReceipt makes later TransactionReceipt calls return ethereum.NotFound,
// simulating a reorg-dropped transaction
func (f *FakeClient) RemoveReceipt(txHash common.Hash) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Receipts, txHash)
}

func (f *FakeClient) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []types.Log
	for _, l := range f.Logs {
		if q.Addresses != nil {
			match := false
			for _, a := range q.Addresses {
				if a == l.Address {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if l.BlockNumber < fromBlockUint(q.FromBlock) {
			continue
		}
		if q.ToBlock != nil && l.BlockNumber > q.ToBlock.Uint64() {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

func (f *FakeClient) CodeAt(ctx context.Context, address common.Address, blockNumber *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Code[address], nil
}

func fromBlockUint(b *big.Int) uint64 {
	if b == nil {
		return 0
	}
	return b.Uint64()
}

var _ Client = (*FakeClient)(nil)
