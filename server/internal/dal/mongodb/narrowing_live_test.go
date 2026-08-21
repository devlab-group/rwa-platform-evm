package mongodb

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/rwa-platform/server/internal/dal/models"
)

// idSet collapses a query result to the ids it returned, which is all these
// tests assert on.
func idSet[T any](items []*T, id func(*T) string) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[id(it)] = true
	}
	return out
}

// TestLiveTransactionNarrowingQueries is the live-Mongo half of
// internal/dal/memory's transaction_narrowing_test. The reconcile tickers
// query for their work instead of reading whole collections, and every one
// of those filters leans on BSON semantics the in-memory adapter cannot
// reproduce: $nin over a named string type, a missing (omitempty) field
// versus $gt "", and a status/blockNumber $or. The keyset-pagination bug
// pageAll above was written for is exactly this failure mode — correct in
// memory, silently empty on Mongo — so the filters are pinned here too.
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

	// Replaces is omitempty, so the six non-replacement documents have no
	// such field at all — a missing field must not satisfy $gt "".
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

// TestLiveKYCEventListAppliedWithTx pins the deep-reorg reopen scan's
// filter: terminal-Applied events are the only ones a reorg can force back
// open, and only those that actually carry a transaction id — TxID is
// omitempty, so the "no tx" case is a missing field again.
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

// TestLiveEnsureIndexesDeclaresNarrowingIndexes runs the real index
// bootstrap and asserts the two indexes the narrowing queries were added
// with actually exist — a malformed spec is a server-side error nothing
// else in the suite would surface.
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
	// Mongo names an unnamed index after its key spec, so the name is the
	// thing to assert on — nested documents decode to bson.D here, not to
	// a map worth digging through.
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
