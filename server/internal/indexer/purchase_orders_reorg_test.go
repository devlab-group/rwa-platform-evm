package indexer_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/sales"
)

// A forward-priced order is projected from the Vault's own logs through the real indexer,
// and a reorg that replaces its closing block moves the order to the status of the new chain
func TestPurchaseOrderFollowsAChainReorgThroughTheIndexer(t *testing.T) {
	ctx := context.Background()
	const chainID = 31337
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	buyer := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	vault := bindings.NewVault()
	round := new(big.Int).Lsh(big.NewInt(1), 70)

	log := func(name string, block uint64, tx byte, data []byte, topics ...common.Hash) types.Log {
		return types.Log{
			Address:     vaultAddr,
			Topics:      append([]common.Hash{vault.ABI.Events[name].ID}, topics...),
			Data:        data,
			BlockNumber: block,
			TxHash:      common.Hash{tx},
			Index:       0,
		}
	}
	pack := func(name string, vals ...any) []byte {
		out, err := vault.ABI.Events[name].Inputs.NonIndexed().Pack(vals...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	id := common.BigToHash(big.NewInt(1))
	b := common.BytesToHash(buyer.Bytes())

	source := indexer.NewFakeSource()
	for n := uint64(1); n <= 5; n++ {
		source.SetHeader(n, 1)
	}
	source.AddLog(log("PurchaseRequested", 2, 0xa1,
		pack("PurchaseRequested", big.NewInt(1000), big.NewInt(2500), common.HexToAddress("0x57"), round, uint64(1700000000)), id, b, b))
	source.AddLog(log("PurchaseSettled", 4, 0xa2, pack("PurchaseSettled", round, big.NewInt(2400), big.NewInt(100)), id))
	source.SetHead(5)

	checkpoints := memory.NewIndexerCheckpointRepository()
	chainEvents := memory.NewChainEventRepository()
	orders := memory.NewPurchaseOrderRepository()
	svc := sales.New(nil, vaultAddr, common.Address{}, memory.NewPurchaseRepository()).WithOrders(orders)
	idx := indexer.New(source, checkpoints, chainEvents, chainID, []common.Address{vaultAddr}, 0, sales.DecodeLog, indexer.WithRollbackWindow(2))

	step := func() *models.PurchaseOrder {
		t.Helper()
		if err := idx.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if err := svc.ReconcileOrders(ctx, chainEvents, chainID, 604800); err != nil {
			t.Fatalf("ReconcileOrders: %v", err)
		}
		o, err := svc.GetOrder(ctx, "1")
		if err != nil {
			t.Fatalf("GetOrder: %v", err)
		}
		return o
	}

	o := step()
	if o.Status != models.PurchaseOrderSettled || o.PricedRound != round.String() || o.QuoteAmount != "2400" || o.Refund != "100" ||
		o.TimeoutAt != 1700000000+604800 || o.Buyer != buyer.Hex() {
		t.Fatalf("order before the reorg = %+v, want Settled at round %s", o, round)
	}

	// blocks 4 and 5 are replaced; on the new chain the buyer cancelled instead of settling
	source.SetHeader(4, 2)
	source.SetHeader(5, 2)
	source.RemoveLogsAtOrAfter(4)
	source.AddLog(log("PurchaseCancelled", 4, 0xa3, pack("PurchaseCancelled", big.NewInt(2500)), id, b))
	source.SetHead(5)

	o = step()
	if o.Status != models.PurchaseOrderCancelled || o.PricedRound != "" || o.QuoteAmount != "" || o.Refund != "2500" {
		t.Errorf("order after the reorg = %+v, want Cancelled with the whole deposit refunded and no pricing round", o)
	}
	if o.CloseTxHash != (common.Hash{0xa3}).Hex() {
		t.Errorf("CloseTxHash = %s, want the new chain's cancel transaction", o.CloseTxHash)
	}
}
