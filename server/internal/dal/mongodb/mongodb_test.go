package mongodb

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/rwa-platform/server/internal/dal/repository"
)

// operandFor returns the $gt or $lt operand from the first $or clause of a keyset
// filter, the strictly-past-this-sort-value comparison
func operandFor(t *testing.T, filter bson.M, sortField, op string) any {
	t.Helper()
	clauses, ok := filter["$or"].(bson.A)
	if !ok || len(clauses) != 2 {
		t.Fatalf("filter has no two-clause $or: %#v", filter)
	}
	first, ok := clauses[0].(bson.M)
	if !ok {
		t.Fatalf("first $or clause is not a document: %#v", clauses[0])
	}
	cmp, ok := first[sortField].(bson.M)
	if !ok {
		t.Fatalf("first $or clause has no %s comparison: %#v", sortField, first)
	}
	v, ok := cmp[op]
	if !ok {
		t.Fatalf("comparison has no %s: %#v", op, cmp)
	}
	return v
}

// TestKeysetFilterDirDateEmitsDateOperand checks that the keyset filter for a Date
// field has a time.Time operand. asset_records.createdAt and
// transactions.submittedAt are stored from time.Time, so they are BSON Dates.
// MongoDB comparisons match only the same BSON type, so an int64 operand matches
// no documents and every page after the first is empty.
//
// This can be checked only on the filter here. The repository unit tests use
// internal/dal/memory, where both sides are int64, and the live tests in this
// package cover the query against MongoDB.
func TestKeysetFilterDirDateEmitsDateOperand(t *testing.T) {
	want := time.Date(2026, 7, 25, 12, 34, 56, 789_000_000, time.UTC)
	cursor := repository.EncodeKeysetCursor(repository.KeysetCursor{SortValue: want.UnixNano(), ID: "rec-1"})

	for _, sortField := range []string{"createdAt", "submittedAt"} {
		got := operandFor(t, keysetFilterDirDate(cursor, sortField, false), sortField, "$gt")
		ts, ok := got.(time.Time)
		if !ok {
			t.Fatalf("%s operand is %T (%v), want time.Time (an int64 never matches a BSON Date)", sortField, got, got)
		}
		if !ts.Equal(want) {
			t.Errorf("%s operand = %v, want %v", sortField, ts, want)
		}
	}
}

// TestKeysetFilterDirKeepsNumericOperand checks that purchases.blockNumber and
// redemption_requests.createdAt, which are stored as numbers, still compare against
// the raw int64
func TestKeysetFilterDirKeepsNumericOperand(t *testing.T) {
	cursor := repository.EncodeKeysetCursor(repository.KeysetCursor{SortValue: 4242, ID: "0xabc"})

	got := operandFor(t, keysetFilterDir(cursor, "blockNumber", true), "blockNumber", "$lt")
	if v, ok := got.(int64); !ok || v != 4242 {
		t.Fatalf("blockNumber operand = %T(%v), want int64(4242)", got, got)
	}
	got = operandFor(t, keysetFilter(cursor, "createdAt"), "createdAt", "$lt")
	if v, ok := got.(int64); !ok || v != 4242 {
		t.Fatalf("createdAt operand = %T(%v), want int64(4242)", got, got)
	}
}

// TestKeysetFilterNoCursorIsUnfiltered checks that an absent or malformed cursor
// gives an empty filter in every variant. A comparison against a zero sort value
// would drop the whole first page for the Date variant, where zero is 1970.
func TestKeysetFilterNoCursorIsUnfiltered(t *testing.T) {
	for _, filter := range []bson.M{
		keysetFilterDirDate("", "createdAt", false),
		keysetFilterDirDate("not-a-cursor", "createdAt", false),
		keysetFilterDir("", "blockNumber", true),
	} {
		if len(filter) != 0 {
			t.Errorf("filter with no usable cursor = %#v, want empty", filter)
		}
	}
}
