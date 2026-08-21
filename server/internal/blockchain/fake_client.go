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

// FakeClient is an in-memory Client used by unit tests. It never performs
// network I/O. Callers configure canned responses and inspect SentTxs after
// exercising code under test.
type FakeClient struct {
	mu sync.Mutex

	ChainIDValue *big.Int
	BlockNum     uint64
	// BaseFee is returned in HeaderByNumber; nil simulates a pre-London
	// chain with no EIP-1559 support, forcing legacy tx construction.
	BaseFee *big.Int
	// Headers, when set for a given block number, is returned verbatim by
	// HeaderByNumber instead of the synthesized default — lets a test pin a
	// specific canonical header (and therefore a specific header.Hash()) at
	// a height, or simulate that height no longer resolving at all (a nil
	// map value), to exercise TxManager's reorg detection: a receipt
	// whose BlockHash no longer matches the current canonical header's hash
	// at BlockNumber, or whose block has disappeared entirely.
	Headers map[uint64]*types.Header
	// Nonces is the PENDING nonce (eth_getTransactionCount(...,"pending")):
	// mempool-inclusive, what PendingNonceAt returns.
	Nonces map[common.Address]uint64
	// MinedNonces is the LATEST/mined nonce (eth_getTransactionCount(...,
	// "latest")), what NonceAt(..., nil) returns. Deliberately a separate
	// map from Nonces (not derived from it) so tests can simulate the two
	// diverging — e.g. a stuck pending transaction (Nonces stays ahead of
	// MinedNonces) or a nonce consumed by a transaction TxManager never
	// tracked (MinedNonces jumps ahead of what TxManager itself broadcast) —
	// exactly the inconsistent-pending-nonce scenario that reconciliation
	// exists to handle. Defaults to 0 (== Nonces' zero default) when unset.
	MinedNonces   map[common.Address]uint64
	GasTipCap     *big.Int
	GasPrice      *big.Int
	EstimatedGas  uint64
	CallResponses map[string][]byte // keyed by hex(msg.Data)
	// Calls records every CallContract in order, so a test can assert
	// WHERE a read went, not only what it returned — CallResponses is
	// keyed by calldata alone and so cannot distinguish two contracts
	// exposing the same method.
	Calls []ethereum.CallMsg
	// DefaultCallResponse, if set, is returned by CallContract for any
	// calldata not present in CallResponses, instead of erroring. Handy for
	// tests that need every hasRole/isAllowed-style call in a batch to
	// return the same canned bool without enumerating each exact selector+args.
	DefaultCallResponse []byte
	Receipts            map[common.Hash]*types.Receipt
	// Txs holds canned TransactionByHash responses keyed by hash; an absent
	// hash returns ethereum.NotFound, matching a real node.
	Txs  map[common.Hash]*types.Transaction
	Logs []types.Log
	// Code holds canned CodeAt responses keyed by address; an address
	// absent from this map returns nil (empty bytecode), matching a real
	// node's response for an address with no deployed contract.
	Code map[common.Address][]byte

	SentTxs []*types.Transaction

	// SendTransactionErr, if set, is returned by every SendTransaction call.
	SendTransactionErr error

	// AutoIncrementNonce, if true, makes SendTransaction bump
	// Nonces[sender] to tx.Nonce()+1 — simulating a real node's mempool,
	// where a pending-nonce lookup reflects every broadcast-but-unconfirmed
	// transaction. Off by default (existing tests seed/advance Nonces by
	// hand via SetNonce).
	//
	// Deliberately hooked on SendTransaction, not PendingNonceAt: a
	// concurrency test proving TxManager's per-key lock actually
	// serializes access needs the fake nonce to only advance at the point
	// TxManager itself would have already broadcast (i.e. while its lock
	// is still held) — auto-incrementing inside PendingNonceAt instead
	// would make every call return a unique value on its own (this
	// struct's mutex alone guarantees that), passing even if TxManager's
	// lock were removed entirely and defeating the point of the test.
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

// SetHeader pins the header returned for a given block number, overriding
// the synthesized default so its Hash() is caller-controlled — used to
// simulate a reorg that replaces the canonical block at a height a receipt
// already referenced. Passing a nil header simulates that height no longer
// resolving at all.
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

// SetNonce lets tests seed the pending nonce for an account.
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

// SetMinedNonce lets tests seed the LATEST (mined) nonce for an account,
// independent of the pending nonce set via SetNonce — see MinedNonces.
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

// SetReceipt lets tests mark a transaction as mined with a given status/block.
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

// SetTx lets tests register a transaction (used to expose the observed
// RWAFactory.deploy calldata to post-deploy verification).
func (f *FakeClient) SetTx(txHash common.Hash, tx *types.Transaction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Txs[txHash] = tx
}

// RemoveReceipt lets tests simulate a reorg dropping a previously mined
// transaction: a subsequent TransactionReceipt call returns ethereum.NotFound
// exactly as it would for a transaction that fell out of the canonical chain.
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
