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

// FakeClient is an in-memory Client for unit tests. It does no network I/O.
// Tests set canned responses and inspect SentTxs afterwards.
type FakeClient struct {
	mu sync.Mutex

	ChainIDValue *big.Int
	BlockNum     uint64
	// BaseFee is returned in HeaderByNumber. nil simulates a chain without
	// EIP-1559, which forces legacy transactions.
	BaseFee *big.Int
	// Headers, when set for a block number, is returned as is by HeaderByNumber
	// instead of the synthesized default. A test can pin a header (and so its
	// Hash()) at a height, or use a nil map value to make the height stop
	// resolving. That exercises TxManager's reorg detection: a receipt whose
	// BlockHash no longer matches the canonical header at BlockNumber, or whose
	// block has disappeared.
	Headers map[uint64]*types.Header
	// Nonces is the pending nonce, as in eth_getTransactionCount(..., "pending"),
	// which counts the node's pending transactions. PendingNonceAt returns it.
	Nonces map[common.Address]uint64
	// MinedNonces is the latest (mined) nonce, as in eth_getTransactionCount(...,
	// "latest"). NonceAt(..., nil) returns it. It is separate from Nonces so a
	// test can make the two diverge: a stuck pending transaction (Nonces ahead of
	// MinedNonces), or a nonce used by a transaction TxManager never tracked
	// (MinedNonces ahead of what TxManager broadcast). Both are cases that nonce
	// reconciliation handles. It defaults to 0 when unset.
	MinedNonces   map[common.Address]uint64
	GasTipCap     *big.Int
	GasPrice      *big.Int
	EstimatedGas  uint64
	CallResponses map[string][]byte // keyed by hex(msg.Data)
	// Calls records every CallContract in order, so a test can assert where a
	// read went and not only what it returned. CallResponses is keyed by calldata
	// alone and cannot tell two contracts with the same method apart.
	Calls []ethereum.CallMsg
	// DefaultCallResponse, if set, is returned by CallContract for any calldata
	// missing from CallResponses instead of an error. It lets a test give every
	// hasRole or isAllowed call in a batch the same answer without listing each
	// selector and argument set.
	DefaultCallResponse []byte
	Receipts            map[common.Hash]*types.Receipt
	// Txs holds canned TransactionByHash responses keyed by hash; an absent hash
	// returns ethereum.NotFound, as a node does
	Txs  map[common.Hash]*types.Transaction
	Logs []types.Log
	// Code holds canned CodeAt responses keyed by address; an absent address
	// returns nil (empty bytecode), as a node does for an address with no
	// contract
	Code map[common.Address][]byte

	SentTxs []*types.Transaction

	// SendTransactionErr, if set, is returned by every SendTransaction call.
	SendTransactionErr error

	// AutoIncrementNonce, if true, makes SendTransaction set Nonces[sender] to
	// tx.Nonce()+1, as a node's pending nonce reflects every broadcast but
	// unconfirmed transaction. It is off by default, and tests then set Nonces
	// with SetNonce.
	//
	// It acts on SendTransaction, not PendingNonceAt, so that a concurrency test
	// of TxManager's per-key lock sees the nonce advance only after TxManager
	// has broadcast, while its lock is still held. Incrementing inside
	// PendingNonceAt would give every call a unique value through this struct's
	// mutex alone, so the test would pass even with the lock removed.
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

// SetHeader pins the header returned for a block number, replacing the
// synthesized default so the test controls its Hash(). It simulates a reorg that
// replaces the canonical block at a height a receipt already referenced. A nil
// header makes that height stop resolving.
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

// RemoveReceipt simulates a reorg dropping a mined transaction: later
// TransactionReceipt calls return ethereum.NotFound, as for a transaction that
// left the canonical chain
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
