package blockchain

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

var feeTestTo = common.HexToAddress("0x0000000000000000000000000000000000dEaD")

// Legacy fixture: 100000 gas + 20% buffer = 120000; at 2 gwei, maximum fee = 240000 gwei
const (
	feeTestGasPrice = 2_000_000_000
	feeTestGasLimit = 120_000
	feeTestMaxCost  = feeTestGasPrice * feeTestGasLimit
)

func TestTxManagerSubmit_FeeCapsAreInclusiveAtTheLimit(t *testing.T) {
	tests := []struct {
		name    string
		caps    FeeCaps
		value   int64
		wantErr string
	}{
		{name: "max fee per gas equal to the price", caps: FeeCaps{MaxFeePerGas: big.NewInt(feeTestGasPrice)}},
		{name: "max fee per gas one wei under the price", caps: FeeCaps{MaxFeePerGas: big.NewInt(feeTestGasPrice - 1)}, wantErr: "MaxFeePerGas"},
		{name: "max total cost equal to gas times price", caps: FeeCaps{MaxTotalCost: big.NewInt(feeTestMaxCost)}},
		{name: "max total cost one wei under", caps: FeeCaps{MaxTotalCost: big.NewInt(feeTestMaxCost - 1)}, wantErr: "MaxTotalCost"},
		{name: "transferred value counts toward the total", caps: FeeCaps{MaxTotalCost: big.NewInt(feeTestMaxCost)}, value: 1, wantErr: "MaxTotalCost"},
		{name: "value plus fees exactly at the total", caps: FeeCaps{MaxTotalCost: big.NewInt(feeTestMaxCost + 5)}, value: 5},
		{name: "nil caps disable both limits", caps: FeeCaps{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priv, _ := crypto.GenerateKey()
			client := NewFakeClient()
			mgr := NewTxManagerWithFeeCaps(client, memory.NewTransactionRepository(), big.NewInt(31337), FeeModeLegacy, tt.caps)

			_, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: feeTestTo, Value: big.NewInt(tt.value)})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Submit: %v", err)
				}
				if len(client.SentTxs) != 1 || client.SentTxs[0].Gas() != feeTestGasLimit {
					t.Fatalf("sent %d txs, want 1 with gas %d", len(client.SentTxs), feeTestGasLimit)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Submit error = %v, want it to mention %s", err, tt.wantErr)
			}
			if len(client.SentTxs) != 0 {
				t.Errorf("a rejected fee still broadcast %d txs", len(client.SentTxs))
			}
		})
	}
}

// 1559 fixture: base fee 1 gwei and tip 1 gwei give a fee cap of 2*1+1 = 3 gwei
const (
	bumpTestTip    = 1_000_000_000
	bumpTestFeeCap = 3_000_000_000
)

func submitEIP1559(t *testing.T, caps FeeCaps) (TxManager, *FakeClient, *models.Transaction, *ecdsa.PrivateKey) {
	t.Helper()
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	mgr := NewTxManagerWithFeeCaps(client, memory.NewTransactionRepository(), big.NewInt(31337), FeeModeEIP1559, caps)
	rec, err := mgr.Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: feeTestTo})
	if err != nil {
		t.Fatal(err)
	}
	return mgr, client, rec, priv
}

func TestTxManagerReplace_EIP1559BumpsBothCapsKeepsNonceAndGas(t *testing.T) {
	mgr, client, rec, priv := submitEIP1559(t, FeeCaps{})
	orig := client.SentTxs[0]
	if orig.Type() != types.DynamicFeeTxType || orig.GasTipCap().Int64() != bumpTestTip || orig.GasFeeCap().Int64() != bumpTestFeeCap {
		t.Fatalf("fixture: original = type %d tip %s cap %s", orig.Type(), orig.GasTipCap(), orig.GasFeeCap())
	}

	repl, err := mgr.Replace(context.Background(), rec.ID, NewStaticKeySigner(priv), 20)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(client.SentTxs) != 2 {
		t.Fatalf("sent %d txs, want 2", len(client.SentTxs))
	}
	got := client.SentTxs[1]
	if got.Type() != types.DynamicFeeTxType || got.Nonce() != orig.Nonce() || repl.Nonce != rec.Nonce {
		t.Errorf("replacement type %d nonce %d (record %d), want a 1559 tx at nonce %d", got.Type(), got.Nonce(), repl.Nonce, orig.Nonce())
	}
	// 20 percent over 1 gwei and 3 gwei
	if got.GasTipCap().Int64() != 1_200_000_000 || got.GasFeeCap().Int64() != 3_600_000_000 {
		t.Errorf("replacement tip %s cap %s, want 1200000000 and 3600000000", got.GasTipCap(), got.GasFeeCap())
	}
	if got.Gas() != orig.Gas() || got.To() == nil || *got.To() != feeTestTo || got.ChainId().Int64() != 31337 {
		t.Errorf("replacement gas/to/chain = %d %v %s, want unchanged %d %s 31337", got.Gas(), got.To(), got.ChainId(), orig.Gas(), feeTestTo)
	}
}

func TestTxManagerReplace_SmallRequestedBumpIsRaisedToTheReplacementMinimum(t *testing.T) {
	mgr, client, rec, priv := submitEIP1559(t, FeeCaps{})
	if _, err := mgr.Replace(context.Background(), rec.ID, NewStaticKeySigner(priv), 1); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got := client.SentTxs[1]
	// Replace enforces a minimum 10% increase in both caps
	if got.GasTipCap().Int64() != 1_100_000_000 || got.GasFeeCap().Int64() != 3_300_000_000 {
		t.Errorf("replacement tip %s cap %s, want 1100000000 and 3300000000", got.GasTipCap(), got.GasFeeCap())
	}
}

func TestTxManagerReplace_BumpedFeesAreCappedInclusively(t *testing.T) {
	tests := []struct {
		name    string
		caps    FeeCaps
		wantErr string
	}{
		{name: "fee cap exactly at the bumped cap", caps: FeeCaps{MaxFeePerGas: big.NewInt(3_600_000_000)}},
		{name: "fee cap one wei under the bumped cap", caps: FeeCaps{MaxFeePerGas: big.NewInt(3_599_999_999)}, wantErr: "bumped fee rejected"},
		{name: "tip cap exactly at the bumped tip", caps: FeeCaps{MaxTipPerGas: big.NewInt(1_200_000_000)}},
		{name: "tip cap one wei under the bumped tip", caps: FeeCaps{MaxTipPerGas: big.NewInt(1_199_999_999)}, wantErr: "bumped tip cap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Submit under no caps, then tighten them for the replacement
			priv, _ := crypto.GenerateKey()
			client := NewFakeClient()
			repo := memory.NewTransactionRepository()
			rec, err := NewTxManager(client, repo, big.NewInt(31337), FeeModeEIP1559).Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: feeTestTo})
			if err != nil {
				t.Fatal(err)
			}
			mgr := NewTxManagerWithFeeCaps(client, repo, big.NewInt(31337), FeeModeEIP1559, tt.caps)

			_, err = mgr.Replace(context.Background(), rec.ID, NewStaticKeySigner(priv), 20)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Replace: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Replace error = %v, want it to contain %q", err, tt.wantErr)
			}
			if len(client.SentTxs) != 1 {
				t.Errorf("a rejected bump still broadcast: %d txs sent", len(client.SentTxs))
			}
			orig, err := repo.Get(context.Background(), rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if orig.Status != models.TxPending {
				t.Errorf("original status = %s, want it left pending", orig.Status)
			}
		})
	}
}

func TestTxManagerReplace_FeeCapNeverFallsBelowTipCap(t *testing.T) {
	mgr, client, rec, priv := submitEIP1559(t, FeeCaps{})
	// The tip suggestion jumps far above the original fee cap, and the base fee collapses
	client.GasTipCap = big.NewInt(10_000_000_000)
	client.BaseFee = big.NewInt(1)
	if _, err := mgr.Replace(context.Background(), rec.ID, NewStaticKeySigner(priv), 10); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got := client.SentTxs[1]
	if got.GasFeeCap().Cmp(got.GasTipCap()) < 0 {
		t.Errorf("fee cap %s below tip cap %s: a node rejects this transaction", got.GasFeeCap(), got.GasTipCap())
	}
	if got.GasTipCap().Int64() != 11_000_000_000 {
		t.Errorf("tip cap = %s, want 11000000000 (10 percent over the 10 gwei suggestion)", got.GasTipCap())
	}
}

func TestTxManagerReplace_ModeSwitchIgnoresTheOriginalsFeeFields(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	repo := memory.NewTransactionRepository()
	rec, err := NewTxManager(client, repo, big.NewInt(31337), FeeModeLegacy).Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: feeTestTo})
	if err != nil {
		t.Fatal(err)
	}
	// The same record is now replaced by a manager that builds 1559 transactions
	mgr := NewTxManager(client, repo, big.NewInt(31337), FeeModeEIP1559)
	if _, err := mgr.Replace(context.Background(), rec.ID, NewStaticKeySigner(priv), 20); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got := client.SentTxs[1]
	if got.Type() != types.DynamicFeeTxType || got.GasTipCap().Int64() != 1_200_000_000 || got.GasFeeCap().Int64() != 3_600_000_000 {
		t.Errorf("replacement type %d tip %s cap %s, want 1559 bumped from the current suggestion only", got.Type(), got.GasTipCap(), got.GasFeeCap())
	}
}

// headerErrClient fails every header lookup with err
type headerErrClient struct {
	*FakeClient
	err error
}

func (c headerErrClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return nil, c.err
}

// confirmedWithBlockHash supplies a nonzero receipt hash so RefreshStatuses checks the canonical header
func confirmedWithBlockHash(t *testing.T) (*FakeClient, *memory.TransactionRepository, *models.Transaction, func(Client) TxManager) {
	t.Helper()
	priv, _ := crypto.GenerateKey()
	client := NewFakeClient()
	repo := memory.NewTransactionRepository()
	mk := func(c Client) TxManager { return NewTxManager(c, repo, big.NewInt(31337), FeeModeLegacy) }
	rec, err := mk(client).Submit(context.Background(), SubmitRequest{PrivateKey: priv, To: feeTestTo})
	if err != nil {
		t.Fatal(err)
	}
	minedAt := big.NewInt(int64(client.BlockNum))
	header := &types.Header{Number: minedAt, Extra: []byte("canonical")}
	client.SetHeader(minedAt.Uint64(), header)
	client.SetReceipt(common.HexToHash(rec.TxHash), &types.Receipt{
		Status: types.ReceiptStatusSuccessful, BlockNumber: minedAt, BlockHash: header.Hash(),
	})
	return client, repo, rec, mk
}

func TestTxManagerRefreshStatuses_BlockThatNoLongerResolvesIsReorged(t *testing.T) {
	client, repo, rec, mk := confirmedWithBlockHash(t)
	client.SetHeader(client.BlockNum, nil) // the height is gone

	if err := mk(client).RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(context.Background(), rec.ID)
	if got.Status != models.TxReorged {
		t.Errorf("Status = %s, want reorged when the receipt's block no longer resolves", got.Status)
	}
}

func TestTxManagerRefreshStatuses_HeaderLookupFailureLeavesStatusUntouched(t *testing.T) {
	client, repo, rec, mk := confirmedWithBlockHash(t)
	boom := errors.New("rpc down")

	err := mk(headerErrClient{FakeClient: client, err: boom}).RefreshStatuses(context.Background(), 3)
	if !errors.Is(err, boom) {
		t.Fatalf("RefreshStatuses error = %v, want %v", err, boom)
	}
	got, _ := repo.Get(context.Background(), rec.ID)
	if got.Status == models.TxReorged || got.Status == models.TxConfirmed {
		t.Errorf("Status = %s after an RPC failure, want it unchanged", got.Status)
	}
}

func TestTxManagerRefreshStatuses_MatchingCanonicalBlockIsMinedNotReorged(t *testing.T) {
	client, repo, rec, mk := confirmedWithBlockHash(t)
	if err := mk(client).RefreshStatuses(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(context.Background(), rec.ID)
	if got.Status != models.TxMined {
		t.Errorf("Status = %s, want mined for a receipt whose block is canonical and not yet 3 deep", got.Status)
	}
}
