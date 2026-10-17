package mongodb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

// liveDB uses MONGO_TEST_URI and the throwaway rwa_pagination_test database, dropped
// during cleanup. Without the variable, live tests skip. The e2e harness can supply
// MongoDB.
func liveDB(t *testing.T) *mongo.Database {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping live-Mongo pagination test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect %s: %v", uri, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("ping %s: %v", uri, err)
	}
	db := client.Database("rwa_pagination_test")
	t.Cleanup(func() {
		bg := context.Background()
		_ = db.Drop(bg)
		_ = client.Disconnect(bg)
	})
	return db
}

// pageAll follows cursors and requires full nonterminal pages, with a guard against
// non-terminating walks
func pageAll(t *testing.T, limit int, next func(cursor string) ([]string, string)) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for page := 0; ; page++ {
		if page > 100 {
			t.Fatal("pagination did not terminate after 100 pages")
		}
		got, nc := next(cursor)
		ids = append(ids, got...)
		if nc == "" {
			return ids
		}
		if len(got) != limit {
			t.Fatalf("page %d returned %d items but still handed out a cursor", page, len(got))
		}
		cursor = nc
	}
}

// TestLiveAssetRecordListPagePagesPastTheFirstPage verifies pagination over createdAt
// BSON Dates. An int64 operand would empty later pages, a type mismatch the memory
// adapter cannot expose.
func TestLiveAssetRecordListPagePagesPastTheFirstPage(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &assetRecordRepo{coll: db.Collection(collAssetRecords)}

	const total, limit = 7, 3
	base := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	for i := range total {
		rec := &models.AssetRecord{
			RecordID:  string(rune('a' + i)),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := repo.Create(ctx, rec); err != nil {
			t.Fatalf("create record %d: %v", i, err)
		}
	}

	ids := pageAll(t, limit, func(cursor string) ([]string, string) {
		items, next, err := repo.ListPage(ctx, cursor, limit)
		if err != nil {
			t.Fatalf("ListPage(%q): %v", cursor, err)
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.RecordID)
		}
		return out, next
	})

	want := []string{"a", "b", "c", "d", "e", "f", "g"} // createdAt ascending
	if len(ids) != len(want) {
		t.Fatalf("paged through %d records (%v), want all %d", len(ids), ids, len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("paged order = %v, want %v", ids, want)
		}
	}
}

// TestLiveTransactionListPagePagesPastTheFirstPage is the same check for
// transactions.submittedAt, the other BSON Date sort field
func TestLiveTransactionListPagePagesPastTheFirstPage(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &transactionRepo{coll: db.Collection(collTransactions)}

	const total, limit = 5, 2
	base := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	for i := range total {
		tx := &models.Transaction{
			ID:          string(rune('a' + i)),
			SubmittedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := repo.Create(ctx, tx); err != nil {
			t.Fatalf("create tx %d: %v", i, err)
		}
	}

	ids := pageAll(t, limit, func(cursor string) ([]string, string) {
		items, next, err := repo.ListPage(ctx, "", cursor, limit)
		if err != nil {
			t.Fatalf("ListPage(%q): %v", cursor, err)
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out, next
	})

	want := []string{"a", "b", "c", "d", "e"} // submittedAt ascending
	if len(ids) != len(want) {
		t.Fatalf("paged through %d transactions (%v), want all %d", len(ids), ids, len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("paged order = %v, want %v", ids, want)
		}
	}
}

// TestLivePurchaseListPageWalksTiedBlockNumbers requires the ID tie-break to return each purchase once
func TestLivePurchaseListPageWalksTiedBlockNumbers(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &purchaseRepo{coll: db.Collection(collPurchases)}
	for _, id := range []string{"a:0", "b:0", "c:0", "d:0", "e:0"} {
		if err := repo.Create(ctx, &models.Purchase{ID: id, TxHash: id, BlockNumber: 7}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	ids := pageAll(t, 2, func(cursor string) ([]string, string) {
		items, next, err := repo.ListPage(ctx, cursor, 2)
		if err != nil {
			t.Fatalf("ListPage(%q): %v", cursor, err)
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out, next
	})
	if got, want := strings.Join(ids, ","), "e:0,d:0,c:0,b:0,a:0"; got != want {
		t.Fatalf("paged order = %s, want %s", got, want)
	}
}
