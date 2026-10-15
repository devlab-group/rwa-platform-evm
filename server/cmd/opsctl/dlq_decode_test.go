package main

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Replay a typed Minted log through buildIndexer/serverwiring.StrictDecoder into the
// event consumed by projectors
func TestRunDLQRetryTypedDecodeReplaysIntoProjector(t *testing.T) {
	repos := newTestRepos()
	client := blockchain.NewFakeClient()
	controllerAddr := common.HexToAddress("0x00000000000000000000000000000000000C0D10")
	cfg := config.Config{ChainID: 31337}
	addrs := models.Addresses{SupplyController: controllerAddr.Hex()}
	ctx := context.Background()

	header, err := client.HeaderByNumber(ctx, big.NewInt(5))
	if err != nil {
		t.Fatal(err)
	}
	logEntry := buildMintedLog(t, controllerAddr, header.Hash())
	seedDLQ(t, repos, "dlq-minted", logEntry)

	o := testOpsCtx(repos)
	if err := runDLQRetry(o, buildIndexer(cfg, addrs, repos, client), "dlq-minted"); err != nil {
		t.Fatalf("runDLQRetry: %v", err)
	}

	entry, err := repos.IndexerDeadLetters.Get(ctx, "dlq-minted")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Resolved {
		t.Fatal("a typed-decodable entry must resolve on retry")
	}
	evs, err := repos.ChainEvents.ListByName(ctx, 31337, controllerAddr.Hex(), "Minted")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected exactly one replayed Minted event, got %d", len(evs))
	}
}

// A generic-only log stays unresolved: StrictDecoder errors, RetryDeadLetter re-records
// failure, and no generic event is inserted
func TestRunDLQRetryRefusesGenericDecode(t *testing.T) {
	repos := newTestRepos()
	client := blockchain.NewFakeClient()
	controllerAddr := common.HexToAddress("0x00000000000000000000000000000000000C0D10")
	cfg := config.Config{ChainID: 31337}
	addrs := models.Addresses{SupplyController: controllerAddr.Hex()}
	ctx := context.Background()

	header, err := client.HeaderByNumber(ctx, big.NewInt(5))
	if err != nil {
		t.Fatal(err)
	}
	// A canonical (block-hash-matching) log the typed decoder does not recognize
	logEntry := types.Log{
		Address: controllerAddr, BlockNumber: 5, BlockHash: header.Hash(),
		TxHash: common.HexToHash("0xbeef"), Index: 0,
		Topics: []common.Hash{common.HexToHash("0xdeadbeef")},
	}
	seedDLQ(t, repos, "dlq-generic", logEntry)

	o := testOpsCtx(repos)
	// runDLQRetry returns nil after attempting retry, but StrictDecoder's refusal leaves
	// the entry unresolved
	if err := runDLQRetry(o, buildIndexer(cfg, addrs, repos, client), "dlq-generic"); err != nil {
		t.Fatalf("runDLQRetry: %v", err)
	}
	entry, err := repos.IndexerDeadLetters.Get(ctx, "dlq-generic")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Resolved {
		t.Fatal("a generic/topic-only decode must NOT resolve the DLQ entry")
	}
	evs, _ := repos.ChainEvents.ListByName(ctx, 31337, controllerAddr.Hex(), "unknown")
	if len(evs) != 0 {
		t.Fatalf("no generic event may be inserted, got %d", len(evs))
	}
}

// Refuse `dlq retry` before chain access when no typed contract is configured
func TestDLQRetryRefusedWithoutTypedContract(t *testing.T) {
	repos := newTestRepos()
	// No configured typed addresses leaves KnownEventNames empty
	if err := repos.Projects.Upsert(context.Background(), &models.Project{Status: models.ProjectStatusActive}); err != nil {
		t.Fatal(err)
	}
	o := testOpsCtx(repos)
	err := dispatch(o, config.Config{ChainID: 31337}, "dlq", "retry", []string{"--id=whatever"})
	if err == nil {
		t.Fatal("expected dlq retry to be refused when only the generic decoder is available")
	}
}

func buildMintedLog(t *testing.T, addr common.Address, blockHash common.Hash) types.Log {
	t.Helper()
	controller := bindings.NewSupplyController()
	data, err := controller.ABI.Events["Minted"].Inputs.NonIndexed().Pack(big.NewInt(100), big.NewInt(1), common.HexToAddress("0x00000000000000000000000000000000000A0D17"))
	if err != nil {
		t.Fatal(err)
	}
	vault := common.HexToAddress("0x0000000000000000000000000000000000005a17")
	return types.Log{
		Address: addr, BlockNumber: 5, BlockHash: blockHash,
		TxHash: common.HexToHash("0xa11ce"), Index: 0,
		Topics: []common.Hash{
			controller.EventID("Minted"),
			common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
			common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
			common.BytesToHash(vault.Bytes()),
		},
		Data: data,
	}
}

func seedDLQ(t *testing.T, repos *repository.Repositories, id string, logEntry types.Log) {
	t.Helper()
	raw, err := json.Marshal(logEntry)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repos.IndexerDeadLetters.Record(context.Background(), &models.DeadLetterEntry{
		ID: id, ChainID: 31337, BlockNumber: logEntry.BlockNumber, BlockHash: logEntry.BlockHash.Hex(),
		TxHash: logEntry.TxHash.Hex(), LogIndex: logEntry.Index, ErrorCategory: models.DLQErrorDecode,
		ErrorMessage: "original typed decode failed", FirstFailedAt: now, LastFailedAt: now, SourceData: raw,
	}); err != nil {
		t.Fatal(err)
	}
}
