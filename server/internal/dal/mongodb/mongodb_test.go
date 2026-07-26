package mongodb

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/rwa-platform/server/internal/dal/repository"
)

// operandFor digs the $gt/$lt operand out of a keyset filter's first $or
// clause, which is where the "strictly past this sort value" comparison lives.
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

// TestKeysetFilterDirDateEmitsDateOperand is the regression test for the
// silently-empty second page: asset_records.createdAt and
// transactions.submittedAt are persisted from Go time.Time values, so they are
// BSON Dates. MongoDB brackets comparisons by type, so an int64 operand against
// a Date field matches zero documents and every page after the first comes back
// empty. The operand must therefore be a time.Time, not the raw cursor int64.
//
// This can only be asserted at the filter-construction level here: the
// repository unit tests run against internal/dal/memory, where both sides are
// the same int64 and compare correctly no matter what — which is exactly why
// the bug survived `go test ./...`. A live-Mongo check belongs in the e2e
// harness.
func TestKeysetFilterDirDateEmitsDateOperand(t *testing.T) {
	want := time.Date(2026, 7, 25, 12, 34, 56, 789_000_000, time.UTC)
	cursor := repository.EncodeKeysetCursor(repository.KeysetCursor{SortValue: want.UnixNano(), ID: "rec-1"})

	for _, sortField := range []string{"createdAt", "submittedAt"} {
		got := operandFor(t, keysetFilterDirDate(cursor, sortField, false), sortField, "$gt")
		ts, ok := got.(time.Time)
		if !ok {
			t.Fatalf("%s operand is %T (%v), want time.Time — an int64 never matches a BSON Date", sortField, got, got)
		}
		if !ts.Equal(want) {
			t.Errorf("%s operand = %v, want %v", sortField, ts, want)
		}
	}
}

// TestKeysetFilterDirKeepsNumericOperand: the numeric collections
// (purchases.blockNumber, redemption_requests.createdAt) store their sort field
// as a number and must keep comparing against the raw int64 — the Date fix must
// not "fix" them into a type mismatch of their own.
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

// TestKeysetFilterNoCursorIsUnfiltered: an absent/garbage cursor is the first
// page, which must be an empty filter in every variant — not a comparison
// against a zero sort value (that would drop the whole first page for the Date
// variant, where zero is 1970).
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
