package txindex

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

const txChainID = int64(31337)

var (
	vaultAddr  = common.HexToAddress("0x0000000000000000000000000000000000000004").Hex()
	tokenAddr  = common.HexToAddress("0x0000000000000000000000000000000000000001").Hex()
	callerAddr = common.HexToAddress("0x00000000000000000000000000000000000000c1").Hex()
	senderAddr = common.HexToAddress("0x00000000000000000000000000000000000000d2").Hex()
)

var evSeq int

// seedEvent assigns distinct IndexedAt values for deterministic SubmittedAt ordering
func seedEvent(t *testing.T, repos *repository.Repositories, addr, txHash, name string, block uint64, logIndex uint, removed bool, data map[string]any) {
	t.Helper()
	evSeq++
	e := &models.ChainEvent{
		ChainID: txChainID, Address: addr, TxHash: txHash, LogIndex: logIndex, BlockNumber: block,
		BlockHash: fmt.Sprintf("0xblock%d", block), Name: name, Data: data, Removed: removed,
		IndexedAt: time.Unix(1_700_000_000, 0).UTC().Add(time.Duration(evSeq) * time.Second),
	}
	if err := repos.ChainEvents.Create(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, repos *repository.Repositories) {
	t.Helper()
	if err := ReconcileStackTransactions(context.Background(), repos.Transactions, repos.ChainEvents, repos.IndexerCheckpoints, txChainID); err != nil {
		t.Fatalf("ReconcileStackTransactions: %v", err)
	}
}

func getEvtTx(t *testing.T, repos *repository.Repositories, txHash string) *models.Transaction {
	t.Helper()
	tx, err := repos.Transactions.Get(context.Background(), models.EventDerivedTxIDPrefix+txHash)
	if err != nil {
		t.Fatalf("get evt tx for %s: %v", txHash, err)
	}
	return tx
}

func TestProjectsProceedsWithdrawn(t *testing.T) {
	repos := memory.New()
	seedEvent(t, repos, vaultAddr, "0xw1", "ProceedsWithdrawn", 10, 0, false,
		map[string]any{"treasury": tokenAddr, "quoteAmount": "5000", "caller": callerAddr})
	run(t, repos)

	tx := getEvtTx(t, repos, "0xw1")
	if tx.Kind != "treasury_withdrawal" {
		t.Errorf("Kind = %q, want treasury_withdrawal", tx.Kind)
	}
	if tx.From != callerAddr {
		t.Errorf("From = %q, want caller %s", tx.From, callerAddr)
	}
	if tx.To != vaultAddr {
		t.Errorf("To = %q, want vault %s", tx.To, vaultAddr)
	}
	if tx.Value != "5000" {
		t.Errorf("Value = %q, want 5000", tx.Value)
	}
	if tx.Status != models.TxConfirmed {
		t.Errorf("Status = %q, want confirmed", tx.Status)
	}
	if tx.BlockNumber != 10 {
		t.Errorf("BlockNumber = %d, want 10", tx.BlockNumber)
	}
}

func TestProjectsRoleGrantedFromSender(t *testing.T) {
	repos := memory.New()
	seedEvent(t, repos, tokenAddr, "0xr1", "RoleGranted", 5, 0, false,
		map[string]any{"role": "0xrole", "account": callerAddr, "sender": senderAddr})
	run(t, repos)

	tx := getEvtTx(t, repos, "0xr1")
	if tx.Kind != "role_granted" {
		t.Errorf("Kind = %q, want role_granted", tx.Kind)
	}
	if tx.From != senderAddr {
		t.Errorf("From = %q, want sender %s (no caller field)", tx.From, senderAddr)
	}
	if tx.Value != "0" {
		t.Errorf("Value = %q, want 0 (role change has no amount)", tx.Value)
	}
}

// Purchased outranks an "unknown" ERC20 Transfer and RoleGranted in the same buy
// transaction; create one txHash-based record with Purchased's Kind
func TestMultiEventTxPicksPrimary(t *testing.T) {
	repos := memory.New()
	txHash := "0xbuy"
	seedEvent(t, repos, tokenAddr, txHash, "unknown", 20, 0, false, map[string]any{"topic0": "0xdead"})
	seedEvent(t, repos, vaultAddr, txHash, "Purchased", 20, 1, false,
		map[string]any{"buyer": callerAddr, "recipient": callerAddr, "tokenAmount": "10", "quoteAmount": "990", "strategy": tokenAddr})
	seedEvent(t, repos, tokenAddr, txHash, "RoleGranted", 20, 2, false,
		map[string]any{"role": "0xrole", "account": callerAddr, "sender": senderAddr})
	run(t, repos)

	all, err := repos.Transactions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d transactions, want exactly 1 for the multi-event tx", len(all))
	}
	tx := getEvtTx(t, repos, txHash)
	if tx.Kind != "purchase" {
		t.Errorf("Kind = %q, want purchase (primary), not the Transfer/role events", tx.Kind)
	}
	if tx.To != vaultAddr {
		t.Errorf("To = %q, want vault (Purchased emitter)", tx.To)
	}
	if tx.From != callerAddr || tx.Value != "990" {
		t.Errorf("From/Value = %q/%q, want buyer/quoteAmount", tx.From, tx.Value)
	}
}

// A manager-owned txHash must not produce a duplicate event-derived record
func TestDedupSkipsManagerTx(t *testing.T) {
	repos := memory.New()
	ctx := context.Background()
	txHash := "0xmint"
	if err := repos.Transactions.Create(ctx, &models.Transaction{
		ID: "mgr-mint-1", ChainID: txChainID, TxHash: txHash, Kind: "assets.relaySignedResult",
		Status: models.TxConfirmed, SignedRawTx: "0xsigned",
	}); err != nil {
		t.Fatal(err)
	}
	seedEvent(t, repos, tokenAddr, txHash, "Minted", 30, 0, false,
		map[string]any{"recordKey": "0xrec", "amount": "1000", "vault": vaultAddr, "auditor": callerAddr})
	run(t, repos)

	all, err := repos.Transactions.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "mgr-mint-1" {
		t.Fatalf("expected only the manager record, got %d: %+v", len(all), all)
	}
	if _, err := repos.Transactions.Get(ctx, models.EventDerivedTxIDPrefix+txHash); err != repository.ErrNotFound {
		t.Errorf("an evt: record was created for a manager-owned txHash, err=%v", err)
	}
}

// Soft-removing or deleting all backing events marks the derived record TxReorged
// without deleting it
func TestReorgFlipsToReorged(t *testing.T) {
	repos := memory.New()
	ctx := context.Background()
	seedEvent(t, repos, vaultAddr, "0xw2", "ProceedsWithdrawn", 40, 0, false,
		map[string]any{"treasury": tokenAddr, "quoteAmount": "7", "caller": callerAddr})
	run(t, repos)
	if getEvtTx(t, repos, "0xw2").Status != models.TxConfirmed {
		t.Fatal("expected confirmed before reorg")
	}

	// simulate a deep rollback that deletes the backing event
	if _, err := repos.ChainEvents.DeleteFromBlock(ctx, txChainID, vaultAddr, 40); err != nil {
		t.Fatal(err)
	}
	run(t, repos)
	if got := getEvtTx(t, repos, "0xw2").Status; got != models.TxReorged {
		t.Fatalf("Status after rollback = %q, want reorged", got)
	}

	// also cover the soft-removed path on a different tx
	seedEvent(t, repos, vaultAddr, "0xw3", "ProceedsWithdrawn", 41, 0, false,
		map[string]any{"treasury": tokenAddr, "quoteAmount": "8", "caller": callerAddr})
	run(t, repos)
	seedEvent(t, repos, vaultAddr, "0xw3", "ProceedsWithdrawn", 41, 0, true, // same key, now removed
		map[string]any{"treasury": tokenAddr, "quoteAmount": "8", "caller": callerAddr})
	run(t, repos)
	if got := getEvtTx(t, repos, "0xw3").Status; got != models.TxReorged {
		t.Fatalf("Status after soft-remove = %q, want reorged", got)
	}
}

func TestIdempotentAcrossRuns(t *testing.T) {
	repos := memory.New()
	seedEvent(t, repos, vaultAddr, "0xw4", "ProceedsWithdrawn", 50, 0, false,
		map[string]any{"treasury": tokenAddr, "quoteAmount": "9", "caller": callerAddr})
	run(t, repos)
	first := getEvtTx(t, repos, "0xw4")
	run(t, repos)
	run(t, repos)
	second := getEvtTx(t, repos, "0xw4")

	all, err := repos.Transactions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("repeated runs produced %d records, want 1 (stable ID)", len(all))
	}
	if !first.SubmittedAt.Equal(second.SubmittedAt) {
		t.Errorf("SubmittedAt drifted across runs: %v -> %v", first.SubmittedAt, second.SubmittedAt)
	}
}

// ReconciliationRequired returns nil without writes during indexer recovery
func TestFreezesWhileReconciliationRequired(t *testing.T) {
	repos := memory.New()
	ctx := context.Background()
	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: txChainID, Address: indexer.CheckpointAddress, ReconciliationRequired: true,
	}); err != nil {
		t.Fatal(err)
	}
	seedEvent(t, repos, vaultAddr, "0xw5", "ProceedsWithdrawn", 60, 0, false,
		map[string]any{"treasury": tokenAddr, "quoteAmount": "1", "caller": callerAddr})
	run(t, repos)

	all, err := repos.Transactions.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("projector wrote %d records while frozen mid-reorg, want 0", len(all))
	}
}

// An ERC-20 Transfer is indexed for income snapshots and is not history: a plain
// wallet-to-wallet transfer produces no record, and a transaction that also carries
// a business event keeps that event as its primary
func TestTransferLogsAreNotTransactionHistory(t *testing.T) {
	repos := memory.New()
	seedEvent(t, repos, tokenAddr, "0xplain", "Transfer", 10, 0, false,
		map[string]any{"from": callerAddr, "to": senderAddr, "value": "5"})
	seedEvent(t, repos, tokenAddr, "0xmint", "Transfer", 11, 0, false,
		map[string]any{"from": common.Address{}.Hex(), "to": vaultAddr, "value": "5"})
	seedEvent(t, repos, tokenAddr, "0xmint", "Minted", 11, 1, false, map[string]any{"amount": "5"})
	run(t, repos)

	if _, err := repos.Transactions.Get(context.Background(), models.EventDerivedTxIDPrefix+"0xplain"); err == nil {
		t.Error("a plain Transfer became a transaction record")
	}
	if tx := getEvtTx(t, repos, "0xmint"); tx.Kind != "mint" {
		t.Errorf("mint transaction Kind = %q, want mint", tx.Kind)
	}
}

func TestIncomeEventsAreMapped(t *testing.T) {
	repos := memory.New()
	seedEvent(t, repos, vaultAddr, "0xc1", "IncomeClaimed", 10, 0, false, map[string]any{"id": "0", "account": callerAddr, "amount": "75"})
	seedEvent(t, repos, vaultAddr, "0xc2", "DistributionCreated", 11, 0, false, map[string]any{"id": "0", "caller": senderAddr, "total": "100"})
	seedEvent(t, repos, vaultAddr, "0xc3", "MinPurchaseQuoteChanged", 12, 0, false, map[string]any{"caller": senderAddr, "newMinimum": "9"})
	run(t, repos)

	for hash, kind := range map[string]string{"0xc1": "income_claimed", "0xc2": "income_distribution_created", "0xc3": "min_purchase_changed"} {
		if got := getEvtTx(t, repos, hash).Kind; got != kind {
			t.Errorf("%s Kind = %q, want %q", hash, got, kind)
		}
	}
	if tx := getEvtTx(t, repos, "0xc1"); tx.From != callerAddr || tx.Value != "75" {
		t.Errorf("claim tx = from %q value %q", tx.From, tx.Value)
	}
	if tx := getEvtTx(t, repos, "0xc2"); tx.Value != "100" {
		t.Errorf("distribution tx value = %q, want its total", tx.Value)
	}
}
