package memory

import (
	"context"
	"testing"

	"github.com/rwa-platform/server/internal/dal/models"
)

// The refresh tick asks the repository for the work rather than for the
// whole collection, so these narrowing queries have to agree with what
// txManager.isFinal/reconcileReplacements would have kept when they
// filtered in Go. Both adapters must answer identically; this pins the
// semantics on the one that can be tested without a live Mongo.
func TestTransactionRepositoryNarrowingQueries(t *testing.T) {
	ctx := context.Background()
	repo := NewTransactionRepository()
	add := func(id string, status models.TxStatus, block uint64, replaces string) {
		if err := repo.Create(ctx, &models.Transaction{ID: id, Status: status, BlockNumber: block, Replaces: replaces}); err != nil {
			t.Fatal(err)
		}
	}
	add("pending", models.TxPending, 0, "")
	add("reorged", models.TxReorged, 90, "")
	add("confirmed-in-window", models.TxConfirmed, 100, "")
	add("confirmed-past-window", models.TxConfirmed, 99, "")
	add("replaced", models.TxReplaced, 50, "")
	add("failed", models.TxFailed, 0, "")
	add("replacement", models.TxPending, 0, "replaced")

	ids := func(txs []*models.Transaction) map[string]bool {
		got := map[string]bool{}
		for _, tx := range txs {
			got[tx.ID] = true
		}
		return got
	}

	unfinalized, err := repo.ListUnfinalized(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := ids(unfinalized)
	// Terminal statuses and a Confirmed record that has sat out its
	// reorg-recheck window must not come back; a Confirmed one still
	// inside the window must.
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

	replacements, err := repo.ListReplacements(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(replacements) != 1 || replacements[0].ID != "replacement" {
		t.Errorf("ListReplacements = %v, want just the replacement record", ids(replacements))
	}

	byStatus, err := repo.ListByStatuses(ctx, models.TxReorged, models.TxFailed)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(byStatus); len(got) != 2 || !got["reorged"] || !got["failed"] {
		t.Errorf("ListByStatuses = %v, want reorged+failed", got)
	}

	none, err := repo.ListByStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("ListByStatuses() with no statuses returned %d records", len(none))
	}
}
