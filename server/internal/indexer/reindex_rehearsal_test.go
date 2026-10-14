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
	"github.com/rwa-platform/server/internal/redemption"
)

// TestReindexRehearsalReconstructsIdenticalReadModel is the chain reindex
// rehearsal on the fake path: FakeSource stands in for the RPC, so no live
// infrastructure is needed (server/ops/reindex_rehearsal.sh is the equivalent
// procedure against a real deployment).
//
// It builds a small RedemptionEscrow event history, indexes and reconciles it (the
// "before" state), then simulates total loss of the server's own index: chain_events
// and the redemption_requests read model are wiped and the indexer checkpoint is
// new, while FakeSource keeps the full log history as a real chain would. Running
// Poll and Reconcile again must rebuild the same read model. That is the premise
// of deriving redemption status only from events (see internal/redemption).
func TestReindexRehearsalReconstructsIdenticalReadModel(t *testing.T) {
	ctx := context.Background()
	const chainID = 31337
	escrowAddr := common.HexToAddress("0x0000000000000000000000000000000000000E")
	escrowABI := bindings.NewRedemptionEscrow()

	source := indexer.NewFakeSource()
	source.SetHeader(1, 1)
	requestedTopics, requestedData := packRedemptionRequested(t, escrowABI, big.NewInt(1),
		common.HexToAddress("0x00000000000000000000000000000000000AAA"), big.NewInt(1000), big.NewInt(950), 1_700_000_000)
	source.AddLog(types.Log{
		Address: escrowAddr, Topics: requestedTopics, Data: requestedData,
		BlockNumber: 1, TxHash: common.HexToHash("0xaa"), Index: 0,
	})
	source.SetHeader(2, 2)
	fundedTopics, fundedData := packRedemptionFunded(t, escrowABI, big.NewInt(1),
		common.HexToAddress("0x00000000000000000000000000000000000BBB"), big.NewInt(950))
	source.AddLog(types.Log{
		Address: escrowAddr, Topics: fundedTopics, Data: fundedData,
		BlockNumber: 2, TxHash: common.HexToHash("0xbb"), Index: 0,
	})
	source.SetHead(2)

	buildAndRun := func(checkpoints *memory.IndexerCheckpointRepository, chainEvents *memory.ChainEventRepository, requests *memory.RedemptionRequestRepository) *models.RedemptionRequest {
		idx := indexer.New(source, checkpoints, chainEvents, chainID, []common.Address{escrowAddr}, 0, redemption.DecodeLog)
		if err := idx.Poll(ctx); err != nil {
			t.Fatalf("Poll: %v", err)
		}
		svc := redemption.New(nil, escrowAddr, requests, common.Address{})
		if err := svc.Reconcile(ctx, chainEvents, chainID, 1209600); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		got, err := svc.Get(ctx, "1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		return got
	}

	// "before": a normal, uninterrupted index
	checkpoints := memory.NewIndexerCheckpointRepository()
	chainEvents := memory.NewChainEventRepository()
	requests := memory.NewRedemptionRequestRepository()
	before := buildAndRun(checkpoints, chainEvents, requests)
	if before.Status != models.RedemptionFunded {
		t.Fatalf("before.Status = %s, want Funded", before.Status)
	}

	// simulate total index loss: drop every indexed event for this address, drop the
	// derived read model, and start from a new (never polled) checkpoint repo, while
	// the underlying chain (source) is untouched
	if _, err := chainEvents.DeleteFromBlock(ctx, chainID, escrowAddr.Hex(), 0); err != nil {
		t.Fatalf("DeleteFromBlock: %v", err)
	}
	if err := requests.DeleteAll(ctx); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if _, err := requests.Get(ctx, "1"); err == nil {
		t.Fatal("redemption request 1 should be gone after DeleteAll")
	}
	freshCheckpoints := memory.NewIndexerCheckpointRepository()

	// "after": replay from scratch against the same underlying chain
	after := buildAndRun(freshCheckpoints, chainEvents, requests)

	if before.ID != after.ID || before.Beneficiary != after.Beneficiary ||
		before.RWAAmount != after.RWAAmount || before.QuoteAmount != after.QuoteAmount ||
		before.Status != after.Status || before.CreatedAt != after.CreatedAt ||
		before.FundedAtBlock != after.FundedAtBlock {
		t.Errorf("reconstructed read model differs from the original:\nbefore = %+v\nafter  = %+v", before, after)
	}
}

func packRedemptionRequested(t *testing.T, escrowABI bindings.RedemptionEscrow, id *big.Int, beneficiary common.Address, rwaAmount, quoteAmount *big.Int, createdAt uint64) ([]common.Hash, []byte) {
	t.Helper()
	event := escrowABI.ABI.Events["RedemptionRequested"]
	data, err := event.Inputs.NonIndexed().Pack(rwaAmount, quoteAmount, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return []common.Hash{event.ID, common.BigToHash(id), common.BytesToHash(beneficiary.Bytes())}, data
}

func packRedemptionFunded(t *testing.T, escrowABI bindings.RedemptionEscrow, id *big.Int, funder common.Address, quoteAmount *big.Int) ([]common.Hash, []byte) {
	t.Helper()
	event := escrowABI.ABI.Events["RedemptionFunded"]
	data, err := event.Inputs.NonIndexed().Pack(quoteAmount)
	if err != nil {
		t.Fatal(err)
	}
	return []common.Hash{event.ID, common.BigToHash(id), common.BytesToHash(funder.Bytes())}, data
}
