package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
)

func testOpsCtx(repos *repository.Repositories) opsCtx {
	return opsCtx{
		ctx: context.Background(), repos: repos, audit: auditlog.New(repos.AuditLogs),
		actor: "opsctl:test", out: &bytes.Buffer{},
	}
}

func newTestRepos() *repository.Repositories { return memory.New() }

// TestRunTxReplace is a smoke test: submit an original transaction,
// then replace it via the same code path opsctl's `tx replace` subcommand
// calls, against in-memory repos and a fake chain client.
func TestRunTxReplace(t *testing.T) {
	repos := newTestRepos()
	client := blockchain.NewFakeClient()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := blockchain.NewStaticKeySigner(key)
	txs := blockchain.NewTxManagerWithFeeCaps(client, repos.Transactions, big.NewInt(31337), blockchain.FeeModeLegacy, blockchain.FeeCaps{})

	ctx := context.Background()
	original, err := txs.Submit(ctx, blockchain.SubmitRequest{
		IdempotencyKey: "opsctl-test-1", Kind: "test", Signer: signer,
		To: common.HexToAddress("0x00000000000000000000000000000000000AAA"), Data: nil, Value: big.NewInt(0),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if original.Status != models.TxPending {
		t.Fatalf("original status = %s, want Pending", original.Status)
	}

	o := testOpsCtx(repos)
	if err := runTxReplace(o, txs, signer, original.ID, 25); err != nil {
		t.Fatalf("runTxReplace: %v", err)
	}

	// Each audited invocation writes an intent AND a linked
	// result (both under the same action), the intent durably before op runs.
	entries, err := repos.AuditLogs.List(ctx, "opsctl", 10)
	if err != nil {
		t.Fatal(err)
	}
	intents, results := countPhases(entries, "tx.replace")
	if intents != 1 || results != 1 {
		t.Fatalf("expected one tx.replace intent and one result, got intents=%d results=%d (%+v)", intents, results, entries)
	}
}

// countPhases counts the intent/result entries for one action.
func countPhases(entries []*models.AuditLogEntry, action string) (intents, results int) {
	for _, e := range entries {
		if e.Action != action {
			continue
		}
		switch e.Metadata["phase"] {
		case "intent":
			intents++
		case "result":
			results++
		}
	}
	return intents, results
}

// TestRunIndexerResetCheckpoint is a smoke test: a reset with the
// operator-supplied trusted hash matching the chain source succeeds and
// clears events after the trusted block; a mismatched hash is rejected
// WITHOUT deleting anything.
func TestRunIndexerResetCheckpoint(t *testing.T) {
	repos := newTestRepos()
	client := blockchain.NewFakeClient()
	addr := common.HexToAddress("0x00000000000000000000000000000000000BBB")
	ctx := context.Background()

	canonicalHeader, err := client.HeaderByNumber(ctx, big.NewInt(10))
	if err != nil {
		t.Fatal(err)
	}
	trustedHash := canonicalHeader.Hash().Hex()

	if err := repos.ChainEvents.Create(ctx, &models.ChainEvent{
		ChainID: 31337, Address: addr.Hex(), Name: "X", TxHash: "0xaa", LogIndex: 0, BlockNumber: 20,
	}); err != nil {
		t.Fatal(err)
	}

	idx := indexer.New(client, repos.IndexerCheckpoints, repos.ChainEvents, 31337, []common.Address{addr}, 0, nil)
	o := testOpsCtx(repos)

	// Wrong hash: rejected, nothing deleted.
	if err := runIndexerResetCheckpoint(o, idx, client, 10, "0xdeadbeef"); err == nil {
		t.Fatal("expected a mismatched trusted hash to be rejected")
	}
	evs, _ := repos.ChainEvents.ListByName(ctx, 31337, addr.Hex(), "X")
	if len(evs) != 1 {
		t.Fatalf("expected the event to survive a rejected reset, got %d", len(evs))
	}

	// Correct hash: succeeds, deletes the event past the trusted block.
	if err := runIndexerResetCheckpoint(o, idx, client, 10, trustedHash); err != nil {
		t.Fatalf("runIndexerResetCheckpoint: %v", err)
	}
	evs, _ = repos.ChainEvents.ListByName(ctx, 31337, addr.Hex(), "X")
	if len(evs) != 0 {
		t.Fatalf("expected the event past the trusted block to be deleted, got %d", len(evs))
	}
	cp, err := repos.IndexerCheckpoints.Get(ctx, 31337, indexer.CheckpointAddress)
	if err != nil || cp.LastBlock != 10 {
		t.Fatalf("checkpoint = %+v err=%v, want LastBlock=10", cp, err)
	}

	// Two invocations (one rejected, one applied), each writing
	// an intent AND a result = 4 entries; both invocations have one intent and
	// one result under the same action.
	entries, err := repos.AuditLogs.List(ctx, "opsctl", 10)
	if err != nil {
		t.Fatal(err)
	}
	intents, results := countPhases(entries, "indexer.resetToTrustedCheckpoint")
	if intents != 2 || results != 2 {
		t.Fatalf("expected 2 intents and 2 results, got intents=%d results=%d (%d entries)", intents, results, len(entries))
	}
}

// TestRunDLQListAndInspect is the DLQ read-path smoke test.
func TestRunDLQListAndInspect(t *testing.T) {
	repos := newTestRepos()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repos.IndexerDeadLetters.Record(ctx, &models.DeadLetterEntry{
		ID: "dlq-1", ChainID: 31337, BlockNumber: 5, ErrorCategory: models.DLQErrorDecode,
		ErrorMessage: "boom", FirstFailedAt: now, LastFailedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	o := testOpsCtx(repos)
	if err := runDLQList(o); err != nil {
		t.Fatalf("runDLQList: %v", err)
	}
	if err := runDLQInspect(o, "dlq-1"); err != nil {
		t.Fatalf("runDLQInspect: %v", err)
	}
	if err := runDLQInspect(o, "does-not-exist"); err == nil {
		t.Fatal("expected inspecting an unknown DLQ id to error")
	}
}

// TestRunDLQRetry is the smoke test for the canonical-hash-checked
// retry path: a DLQ entry whose retained BlockHash still matches the
// chain source's current header at that block is re-ingested and marked
// Resolved.
func TestRunDLQRetry(t *testing.T) {
	repos := newTestRepos()
	client := blockchain.NewFakeClient()
	addr := common.HexToAddress("0x00000000000000000000000000000000000CCC")
	ctx := context.Background()

	header, err := client.HeaderByNumber(ctx, big.NewInt(5))
	if err != nil {
		t.Fatal(err)
	}
	logEntry := types.Log{
		Address: addr, BlockNumber: 5, BlockHash: header.Hash(),
		TxHash: common.HexToHash("0xaaaa"), Index: 0, Topics: []common.Hash{common.HexToHash("0x01")},
	}
	raw, err := json.Marshal(logEntry)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repos.IndexerDeadLetters.Record(ctx, &models.DeadLetterEntry{
		ID: "dlq-retry-1", ChainID: 31337, BlockNumber: 5, BlockHash: header.Hash().Hex(),
		TxHash: logEntry.TxHash.Hex(), LogIndex: 0, ErrorCategory: models.DLQErrorDecode,
		ErrorMessage: "boom", FirstFailedAt: now, LastFailedAt: now, SourceData: raw,
	}); err != nil {
		t.Fatal(err)
	}

	idx := indexer.New(client, repos.IndexerCheckpoints, repos.ChainEvents, 31337, []common.Address{addr}, 0, nil,
		indexer.WithDeadLetterQueue(repos.IndexerDeadLetters))
	o := testOpsCtx(repos)
	if err := runDLQRetry(o, idx, "dlq-retry-1"); err != nil {
		t.Fatalf("runDLQRetry: %v", err)
	}

	entry, err := repos.IndexerDeadLetters.Get(ctx, "dlq-retry-1")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Resolved {
		t.Fatal("expected the DLQ entry to be Resolved after a successful retry")
	}
}

// TestRunDLQDismiss is the smoke test for manual dismissal.
func TestRunDLQDismiss(t *testing.T) {
	repos := newTestRepos()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repos.IndexerDeadLetters.Record(ctx, &models.DeadLetterEntry{
		ID: "dlq-dismiss-1", ChainID: 31337, ErrorCategory: models.DLQErrorDecode,
		ErrorMessage: "boom", FirstFailedAt: now, LastFailedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	o := testOpsCtx(repos)
	if err := runDLQDismiss(o, "dlq-dismiss-1"); err != nil {
		t.Fatalf("runDLQDismiss: %v", err)
	}
	entry, err := repos.IndexerDeadLetters.Get(ctx, "dlq-dismiss-1")
	if err != nil || !entry.Resolved {
		t.Fatalf("expected the entry to be Resolved, got %+v err=%v", entry, err)
	}
}

// TestRunIPFSRetry is a smoke test: a backup destination that has
// never seen the content gets it added and pinned, and the publication
// record's state advances to Replicated once the configured threshold is
// met.
func TestRunIPFSRetry(t *testing.T) {
	repos := newTestRepos()
	ctx := context.Background()
	data := []byte(`{"asset":"opsctl-retry-test"}`)
	cid, err := ipfs.ComputeCIDv1Raw(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Publications.Upsert(ctx, &models.PublicationRecord{
		ID: "pub-1", CID: cid, State: models.PublicationReplicationPending, ReplicationThreshold: 1,
	}); err != nil {
		t.Fatal(err)
	}

	local := ipfs.NewFakeClient()
	backup := ipfs.NewFakeClient()
	rm := ipfs.NewReplicationManager(local, []ipfs.Destination{{Name: "backup", Client: backup}}, 1, repos.Publications)

	o := testOpsCtx(repos)
	if err := runIPFSRetry(o, rm, "pub-1", data); err != nil {
		t.Fatalf("runIPFSRetry: %v", err)
	}
	rec, err := repos.Publications.Get(ctx, "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != models.PublicationReplicated {
		t.Fatalf("State = %s, want Replicated", rec.State)
	}
}

// TestRunIPFSRestoreLocal is a smoke test: content present on a
// backup but missing locally is restored to the local client.
func TestRunIPFSRestoreLocal(t *testing.T) {
	repos := newTestRepos()
	ctx := context.Background()
	data := []byte(`{"asset":"opsctl-restore-test"}`)

	backup := ipfs.NewFakeClient()
	cid, err := backup.AddRaw(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Publications.Upsert(ctx, &models.PublicationRecord{
		ID: "pub-2", CID: cid, State: models.PublicationReplicationFailed, ReplicationThreshold: 1,
	}); err != nil {
		t.Fatal(err)
	}

	local := ipfs.NewFakeClient()
	rm := ipfs.NewReplicationManager(local, []ipfs.Destination{{Name: "backup", Client: backup}}, 1, repos.Publications)

	o := testOpsCtx(repos)
	if err := runIPFSRestoreLocal(o, rm, "pub-2"); err != nil {
		t.Fatalf("runIPFSRestoreLocal: %v", err)
	}
	if _, err := local.Get(ctx, cid); err != nil {
		t.Fatalf("expected the content to be restored to the local client: %v", err)
	}
}

// TestAuditActor pins that the audit-actor label is attributable and
// "opsctl:"-prefixed: an explicit --actor wins, and it never falls through to
// an empty label (opsctl is not credential-gated — this is attribution only).
func TestAuditActor(t *testing.T) {
	if got := auditActor("alice"); got != "opsctl:alice" {
		t.Errorf("auditActor(alice) = %q, want opsctl:alice", got)
	}
	if got := auditActor("  spaced  "); got != "opsctl:spaced" {
		t.Errorf("auditActor trims whitespace: got %q", got)
	}
	// With no flag, it derives from the OS user or falls back to "opsctl";
	// either way the label is non-empty and prefixed.
	got := auditActor("")
	if got == "opsctl:" || len(got) <= len("opsctl:") {
		t.Errorf("auditActor(\"\") = %q, want a non-empty opsctl:<label>", got)
	}
}
