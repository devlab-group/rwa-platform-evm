package mongodb

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/rwa-platform/server/internal/dal/repository"
)

// operandFor extracts the first $or branch's $gt/$lt operand for sort-value assertions
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

// TestKeysetFilterDirDateEmitsDateOperand requires time.Time operands for
// asset_records.createdAt and transactions.submittedAt (BSON Dates). Memory tests
// cannot expose BSON type mismatches; live tests cover queries. See
// keysetFilterDirDate.
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

// TestKeysetFilterNoCursorIsUnfiltered checks that absent or malformed cursors impose
// no boundary in numeric or Date filters
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
