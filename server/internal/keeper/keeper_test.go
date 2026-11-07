package keeper

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
)

// Vault PurchaseStatus values
const (
	statusPending   uint8 = 1
	statusSettled   uint8 = 2
	statusRefunded  uint8 = 3
	statusCancelled uint8 = 4
	statusRejected  uint8 = 5
)

var (
	vaultAddress = common.HexToAddress("0x00000000000000000000000000000000000000aa")
	errTransport = errors.New("connection refused")
)

// revertError has the shape of a node's eth_call revert
type revertError struct{}

func (revertError) Error() string  { return "execution reverted" }
func (revertError) ErrorData() any { return "0x" }
func (revertError) ErrorCode() int { return 3 }

// fakeVault answers the keeper's Vault calls from plain per-order state
type fakeVault struct {
	*blockchain.FakeClient
	abi      bindings.Vault
	statuses []uint8
	// keyed by order id, these replace the normal answer
	settleErr map[uint64]error
	readErr   map[uint64]error
	nextErr   error
	headErr   error
	// nextEmpty answers nextPurchaseId with no data, as an address without that function does
	nextEmpty bool
	reads     map[uint64]int
	// onSimulate runs at each settlePurchase simulation
	onSimulate func()
}

func (v *fakeVault) CallContract(ctx context.Context, msg ethereum.CallMsg, block *big.Int) ([]byte, error) {
	if msg.To == nil || *msg.To != vaultAddress {
		return nil, errors.New("call to an address other than the Vault")
	}
	method, err := v.abi.ABI.MethodById(msg.Data[:4])
	if err != nil {
		return nil, err
	}
	if method.Name == "nextPurchaseId" {
		if v.nextErr != nil || v.nextEmpty {
			return nil, v.nextErr
		}
		return method.Outputs.Pack(big.NewInt(int64(len(v.statuses))))
	}
	args, err := method.Inputs.Unpack(msg.Data[4:])
	if err != nil {
		return nil, err
	}
	id := args[0].(*big.Int).Uint64()
	switch method.Name {
	case "getPurchase":
		v.reads[id]++
		if err := v.readErr[id]; err != nil {
			return nil, err
		}
		return method.Outputs.Pack(bindings.PurchaseOrderTuple{
			Status:         v.statuses[id],
			BaseRound:      big.NewInt(1),
			TokenAmount:    big.NewInt(1),
			MaxQuoteAmount: big.NewInt(1),
		})
	case "settlePurchase":
		if v.onSimulate != nil {
			v.onSimulate()
		}
		return nil, v.settleErr[id]
	}
	return nil, errors.New("unexpected call to " + method.Name)
}

func (v *fakeVault) BlockNumber(ctx context.Context) (uint64, error) {
	if v.headErr != nil {
		return 0, v.headErr
	}
	return v.FakeClient.BlockNumber(ctx)
}

func newFixture(t *testing.T, caps blockchain.FeeCaps, statuses ...uint8) (*Keeper, *fakeVault, common.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	fake := blockchain.NewFakeClient()
	fake.AutoIncrementNonce = true
	vault := &fakeVault{
		FakeClient: fake,
		abi:        bindings.NewVault(),
		statuses:   statuses,
		settleErr:  map[uint64]error{},
		readErr:    map[uint64]error{},
		reads:      map[uint64]int{},
	}
	k := New(vault, blockchain.NewStaticKeySigner(key), big.NewInt(31337), vaultAddress, caps)
	return k, vault, crypto.PubkeyToAddress(key.PublicKey)
}

// sentOrders decodes the order id of every broadcast, failing on anything but settlePurchase
func sentOrders(t *testing.T, v *fakeVault) []uint64 {
	t.Helper()
	ids := []uint64{}
	settle := v.abi.ABI.Methods["settlePurchase"]
	for _, tx := range v.SentTxs {
		if tx.To() == nil || *tx.To() != vaultAddress {
			t.Fatalf("transaction sent to %v, want the Vault", tx.To())
		}
		if string(tx.Data()[:4]) != string(settle.ID) {
			t.Fatalf("transaction selector %x, want settlePurchase", tx.Data()[:4])
		}
		args, err := settle.Inputs.Unpack(tx.Data()[4:])
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, args[0].(*big.Int).Uint64())
	}
	return ids
}

func mustPass(t *testing.T, k *Keeper) {
	t.Helper()
	if err := k.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
}

func wantSent(t *testing.T, v *fakeVault, want ...uint64) {
	t.Helper()
	if want == nil {
		want = []uint64{}
	}
	if got := sentOrders(t, v); !reflect.DeepEqual(got, want) {
		t.Fatalf("settled orders = %v, want %v", got, want)
	}
}

func TestPass_PendingOrders_SettlesEachOnceOldestFirst(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending, statusPending)

	mustPass(t, k)

	wantSent(t, vault, 0, 1)
	if n0, n1 := vault.SentTxs[0].Nonce(), vault.SentTxs[1].Nonce(); n0 != 0 || n1 != 1 {
		t.Fatalf("nonces = %d, %d, want 0, 1", n0, n1)
	}
}

func TestPass_NoOrders_SendsNothing(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{})

	mustPass(t, k)

	wantSent(t, vault)
}

func TestPass_ClosedOrders_SendNothingAndAreNotReadAgain(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusSettled, statusRefunded, statusCancelled, statusRejected)

	mustPass(t, k)
	mustPass(t, k)

	wantSent(t, vault)
	if want := (map[uint64]int{0: 1, 1: 1, 2: 1, 3: 1}); !reflect.DeepEqual(vault.reads, want) {
		t.Fatalf("getPurchase reads = %v, want %v", vault.reads, want)
	}
}

func TestPass_SimulationReverts_SendsNothingAndSettlesOnceItPasses(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	vault.settleErr[0] = revertError{}

	mustPass(t, k)
	wantSent(t, vault)

	delete(vault.settleErr, 0)
	mustPass(t, k)
	wantSent(t, vault, 0)
}

func TestPass_OneOrderFails_OthersStillSettleAndItRetries(t *testing.T) {
	for name, breakOrder := range map[string]func(v *fakeVault){
		"getPurchase read":     func(v *fakeVault) { v.readErr[0] = errTransport },
		"simulation transport": func(v *fakeVault) { v.settleErr[0] = errTransport },
	} {
		t.Run(name, func(t *testing.T) {
			k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending, statusPending)
			breakOrder(vault)

			err := k.Pass(context.Background())

			if !errors.Is(err, errTransport) {
				t.Fatalf("Pass error = %v, want the transport failure", err)
			}
			wantSent(t, vault, 1)

			delete(vault.readErr, 0)
			delete(vault.settleErr, 0)
			mustPass(t, k)
			wantSent(t, vault, 1, 0)
		})
	}
}

func TestPass_NewOrders_FoundOnLaterPasses(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusSettled)
	mustPass(t, k)

	vault.statuses = append(vault.statuses, statusPending)
	mustPass(t, k)

	wantSent(t, vault, 1)
}

func TestPass_NextIdUnreadable_ReportsItAndKeepsWorkingKnownOrders(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	vault.settleErr[0] = revertError{}
	mustPass(t, k)

	delete(vault.settleErr, 0)
	vault.nextErr = errTransport
	err := k.Pass(context.Background())

	if !errors.Is(err, errTransport) {
		t.Fatalf("Pass error = %v, want the transport failure", err)
	}
	wantSent(t, vault, 0)
}

func TestPass_AddressWithoutOrders_IsAnError(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{})
	vault.nextEmpty = true

	if err := k.Pass(context.Background()); err == nil {
		t.Fatal("Pass reported no error")
	}
}

func TestPass_ChainHeadUnreadable_ReportsItAndStillSettles(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	vault.headErr = errTransport

	err := k.Pass(context.Background())

	if !errors.Is(err, errTransport) {
		t.Fatalf("Pass error = %v, want the transport failure", err)
	}
	wantSent(t, vault, 0)
}

func TestPass_UnminedTransaction_NotSentAgain(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)

	mustPass(t, k)
	mustPass(t, k)
	mustPass(t, k)

	wantSent(t, vault, 0)
}

func TestPass_SettledAfterSending_StopsTrackingTheOrder(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	mustPass(t, k)
	vault.SetReceipt(vault.SentTxs[0].Hash(), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(99)})
	vault.statuses[0] = statusSettled

	mustPass(t, k)
	reads := vault.reads[0]
	mustPass(t, k)

	wantSent(t, vault, 0)
	if vault.reads[0] != reads {
		t.Fatalf("order read %d more times after it closed", vault.reads[0]-reads)
	}
}

func TestPass_MinedButOrderStillReadsPending_NotSentAgain(t *testing.T) {
	// the fake chain's head is block 100 and the keeper wants one confirmation
	for name, block := range map[string]int64{"mined at the head": 100, "confirmed": 99} {
		t.Run(name, func(t *testing.T) {
			k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
			mustPass(t, k)
			// a lagging read: the transaction has landed and the order has not caught up
			vault.SetReceipt(vault.SentTxs[0].Hash(), &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(block)})

			mustPass(t, k)
			mustPass(t, k)

			wantSent(t, vault, 0)
		})
	}
}

func TestPass_RevertedTransaction_TriesAgainWithNewNonce(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	mustPass(t, k)
	vault.SetReceipt(vault.SentTxs[0].Hash(), &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: big.NewInt(99)})

	mustPass(t, k)

	wantSent(t, vault, 0, 0)
	if got := vault.SentTxs[1].Nonce(); got != 1 {
		t.Fatalf("retry nonce = %d, want 1", got)
	}
}

func TestPass_NonceTakenByAnotherTransaction_TriesAgain(t *testing.T) {
	k, vault, sender := newFixture(t, blockchain.FeeCaps{}, statusPending)
	mustPass(t, k)
	// an operator cleared the stuck nonce by hand with a transaction the keeper never saw
	vault.SetMinedNonce(sender, 1)

	mustPass(t, k)

	wantSent(t, vault, 0, 0)
}

func TestPass_SlowTransaction_ReplacedOnlyAfterTheWait(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	mustPass(t, k)
	record, err := k.records.Get(context.Background(), k.sent[0])
	if err != nil {
		t.Fatal(err)
	}

	k.now = func() time.Time { return record.SubmittedAt.Add(5 * time.Minute) }
	mustPass(t, k)
	wantSent(t, vault, 0)

	k.now = func() time.Time { return record.SubmittedAt.Add(5*time.Minute + time.Nanosecond) }
	mustPass(t, k)
	wantSent(t, vault, 0, 0)

	first, second := vault.SentTxs[0], vault.SentTxs[1]
	if second.Nonce() != first.Nonce() {
		t.Fatalf("replacement nonce = %d, want %d", second.Nonce(), first.Nonce())
	}
	wantFee := new(big.Int).Div(new(big.Int).Mul(first.GasFeeCap(), big.NewInt(120)), big.NewInt(100))
	if second.GasFeeCap().Cmp(wantFee) < 0 {
		t.Fatalf("replacement fee cap = %s, want at least %s", second.GasFeeCap(), wantFee)
	}

	// the replacement is the transaction tracked from now on
	k.now = time.Now
	mustPass(t, k)
	wantSent(t, vault, 0, 0)
}

func TestPass_FeeCaps_BlockTheSendAndTheBump(t *testing.T) {
	// the fake's base fee and tip give a first fee cap of 3 gwei
	firstFee := big.NewInt(3_000_000_000)

	t.Run("first send above the cap", func(t *testing.T) {
		k, vault, _ := newFixture(t, blockchain.FeeCaps{MaxFeePerGas: new(big.Int).Sub(firstFee, big.NewInt(1))}, statusPending)

		if err := k.Pass(context.Background()); err == nil {
			t.Fatal("Pass reported no error")
		}
		wantSent(t, vault)
	})

	t.Run("bump above the cap", func(t *testing.T) {
		k, vault, _ := newFixture(t, blockchain.FeeCaps{MaxFeePerGas: firstFee}, statusPending)
		mustPass(t, k)
		k.now = func() time.Time { return time.Now().Add(time.Hour) }

		if err := k.Pass(context.Background()); err == nil {
			t.Fatal("Pass reported no error")
		}
		wantSent(t, vault, 0)
	})
}

func TestPass_BroadcastRejected_ReportsItAndTriesAgain(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	rejected := errors.New("insufficient funds for gas * price + value")
	vault.SendTransactionErr = rejected

	err := k.Pass(context.Background())

	if !errors.Is(err, rejected) {
		t.Fatalf("Pass error = %v, want the rejection", err)
	}
	wantSent(t, vault)

	vault.SendTransactionErr = nil
	mustPass(t, k)
	wantSent(t, vault, 0)
}

func TestRun_PassesOnEachTickUntilCancelled(t *testing.T) {
	k, vault, _ := newFixture(t, blockchain.FeeCaps{}, statusPending)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vault.onSimulate = cancel
	done := make(chan struct{})

	go func() {
		k.Run(ctx, time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not reach the order and stop")
	}
}
