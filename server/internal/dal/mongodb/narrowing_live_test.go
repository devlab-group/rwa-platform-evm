package mongodb

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/rwa-platform/server/internal/dal/models"
)

// idSet reduces a query result to its ids, which is all these tests compare
func idSet[T any](items []*T, id func(*T) string) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[id(it)] = true
	}
	return out
}

// TestLiveTransactionNarrowingQueries is the live-MongoDB counterpart of the memory
// package's transaction_narrowing_test. The reconcile tickers query for their work
// and do not read whole collections, and each filter depends on BSON behavior the
// in-memory adapter cannot reproduce: $nin over a named string type, a missing
// (omitempty) field against $gt "", and a status and blockNumber $or. The keyset
// pagination bug that pageAll catches is the same kind of failure, correct in
// memory and empty on MongoDB, so these filters are tested here too.
func TestLiveTransactionNarrowingQueries(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &transactionRepo{coll: db.Collection(collTransactions)}

	add := func(id string, status models.TxStatus, block uint64, replaces string) {
		t.Helper()
		if err := repo.Create(ctx, &models.Transaction{ID: id, Status: status, BlockNumber: block, Replaces: replaces}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add("pending", models.TxPending, 0, "")
	add("reorged", models.TxReorged, 90, "")
	add("confirmed-in-window", models.TxConfirmed, 100, "")
	add("confirmed-past-window", models.TxConfirmed, 99, "")
	add("replaced", models.TxReplaced, 50, "")
	add("failed", models.TxFailed, 0, "")
	add("replacement", models.TxPending, 0, "replaced")

	txID := func(tx *models.Transaction) string { return tx.ID }

	unfinalized, err := repo.ListUnfinalized(ctx, 100)
	if err != nil {
		t.Fatalf("ListUnfinalized: %v", err)
	}
	got := idSet(unfinalized, txID)
	for _, want := range []string{"pending", "reorged", "confirmed-in-window", "replacement"} {
		if !got[want] {
			t.Errorf("ListUnfinalized dropped %s", want)
		}
	}
	for _, unwanted := range []string{"confirmed-past-window", "replaced", "failed"} {
		if got[unwanted] {
			t.Errorf("ListUnfinalized returned finalized %s", unwanted)
		}
	}

	// Replaces is omitempty, so the six non-replacement documents have no such field,
	// and a missing field must not satisfy $gt ""
	replacements, err := repo.ListReplacements(ctx)
	if err != nil {
		t.Fatalf("ListReplacements: %v", err)
	}
	if ids := idSet(replacements, txID); len(ids) != 1 || !ids["replacement"] {
		t.Errorf("ListReplacements = %v, want just the replacement record", ids)
	}

	byStatus, err := repo.ListByStatuses(ctx, models.TxReorged, models.TxFailed)
	if err != nil {
		t.Fatalf("ListByStatuses: %v", err)
	}
	if ids := idSet(byStatus, txID); len(ids) != 2 || !ids["reorged"] || !ids["failed"] {
		t.Errorf("ListByStatuses = %v, want reorged+failed", ids)
	}

	none, err := repo.ListByStatuses(ctx)
	if err != nil {
		t.Fatalf("ListByStatuses(): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListByStatuses() with no statuses returned %d records", len(none))
	}
}

// TestLiveKYCEventListAppliedWithTx checks the filter of the deep-reorg reopen scan:
// only Applied events that have a transaction id. TxID is omitempty, so an event
// without one has no such field.
func TestLiveKYCEventListAppliedWithTx(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &kycEventRepo{coll: db.Collection(collKYCEvents), claims: db.Collection(collKYCClaims)}

	add := func(id string, status models.KYCApplyStatus, txID string) {
		t.Helper()
		e := &models.KYCEvent{ID: id, Provider: "test", EventID: id, PayloadHash: id, ApplyStatus: status, TxID: txID}
		if err := repo.Create(ctx, e); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add("applied-with-tx", models.KYCApplyApplied, "0xtx")
	add("applied-no-tx", models.KYCApplyApplied, "")
	add("accepted-with-tx", models.KYCApplyAccepted, "0xtx")
	add("superseded", models.KYCApplySuperseded, "0xtx")

	items, err := repo.ListAppliedWithTx(ctx)
	if err != nil {
		t.Fatalf("ListAppliedWithTx: %v", err)
	}
	ids := idSet(items, func(e *models.KYCEvent) string { return e.ID })
	if len(ids) != 1 || !ids["applied-with-tx"] {
		t.Errorf("ListAppliedWithTx = %v, want just applied-with-tx", ids)
	}
}

// TestLiveEnsureIndexesDeclaresNarrowingIndexes runs EnsureIndexes and checks that
// the two indexes for the narrowing queries exist, since a malformed index spec is a
// server error that nothing else in the suite would show
func TestLiveEnsureIndexesDeclaresNarrowingIndexes(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	if err := EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	cur, err := db.Collection(collTransactions).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var specs []bson.M
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatalf("decode indexes: %v", err)
	}
	// MongoDB names an index after its key spec, so the name is what to compare, and
	// nested documents decode to bson.D, not a map
	names := map[string]bool{}
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		names[name] = true
	}
	for _, want := range []string{"status_1_blockNumber_1", "replaces_1"} {
		if !names[want] {
			t.Errorf("transactions has no %s index; got %v", want, names)
		}
	}
}

func TestLiveDeadLetterRecordIsAtomicUpsert(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &deadLetterRepo{coll: db.Collection(collIndexerDeadLetters)}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Record(ctx, &models.DeadLetterEntry{ID: "$k", ErrorMessage: "$first", FirstFailedAt: t0, LastFailedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Resolve(ctx, "$k"); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Hour)
	if err := repo.Record(ctx, &models.DeadLetterEntry{ID: "$k", ErrorMessage: "second", FirstFailedAt: t1, LastFailedAt: t1}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "$k")
	if err != nil {
		t.Fatal(err)
	}
	if got.RetryCount != 1 || got.Resolved || got.ErrorMessage != "second" ||
		!got.FirstFailedAt.Equal(t0) || !got.LastFailedAt.Equal(t1) {
		t.Errorf("unexpected entry after repeat: %+v", got)
	}
}
