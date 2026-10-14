package mongodb

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

// liveDB connects to the MongoDB named by MONGO_TEST_URI and returns a throwaway
// database, and skips the test when the variable is unset, so `go test ./...` needs
// no running MongoDB. To run these tests, point it at the e2e harness's instance:
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

// pageAll walks every page a ListPage-style function returns. It fails if the
// cursor never advances or a page is empty.
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

// TestLiveAssetRecordListPagePagesPastTheFirstPage checks against a live MongoDB
// that asset_records pages past the first page. createdAt is a BSON Date, and the
// keyset filter once compared it with an int64 nanosecond operand, which MongoDB
// never matches, so page 2 was empty. The in-memory adapter cannot catch this,
// because both sides are int64 there.
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
