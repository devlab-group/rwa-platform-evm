package repository

import (
	"fmt"
	"testing"
)

type keysetItem struct {
	sortValue int64
	id        string
}

func (i keysetItem) SortValue() int64 { return i.sortValue }
func (i keysetItem) ID() string       { return i.id }

func sortedDescItems(n int) []keysetItem {
	// Descending sortValue, id ties broken descending too — the exact
	// order every ListPage caller in this codebase sorts by before
	// calling KeysetPage.
	items := make([]keysetItem, n)
	for i := 0; i < n; i++ {
		items[i] = keysetItem{sortValue: int64(n - i), id: fmt.Sprintf("id-%04d", n-i)}
	}
	return items
}

func TestKeysetPageFirstPage(t *testing.T) {
	items := sortedDescItems(10)
	page, next := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, "", 3, true)
	if len(page) != 3 || page[0].id != "id-0010" || page[2].id != "id-0008" {
		t.Fatalf("page = %+v", page)
	}
	if next == "" {
		t.Fatal("expected a next cursor since more items remain")
	}
}

// TestKeysetPageWalksEveryItemExactlyOnceOver200Records exercises paging
// across more than 200 records: walking
// page by page via the returned cursor must visit every item exactly
// once, in the declared sorted order, with no gaps or duplicates.
func TestKeysetPageWalksEveryItemExactlyOnceOver200Records(t *testing.T) {
	const total = 237
	const pageSize = 25
	items := sortedDescItems(total)

	seen := map[string]bool{}
	var order []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total { // guard against an infinite loop if the walk is broken
			t.Fatal("walk did not terminate")
		}
		page, next := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, cursor, pageSize, true)
		for _, it := range page {
			if seen[it.id] {
				t.Fatalf("id %s visited twice", it.id)
			}
			seen[it.id] = true
			order = append(order, it.id)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d distinct items, want %d", len(seen), total)
	}
	for i := 0; i < total; i++ {
		want := fmt.Sprintf("id-%04d", total-i)
		if order[i] != want {
			t.Fatalf("order[%d] = %s, want %s (walk order must match the declared sort)", i, order[i], want)
		}
	}
}

// TestKeysetPageStableUnderConcurrentPrepend proves the core advantage of
// keyset over offset cursors: inserting new items AHEAD of
// the walk (the newest-first case — a new purchase/redemption always sorts
// first) does not shift, skip, or duplicate anything in a page the caller
// already has a cursor into, unlike an offset cursor which "row 50" would
// now name a different record for.
func TestKeysetPageStableUnderConcurrentPrepend(t *testing.T) {
	items := sortedDescItems(20) // sortValue 20..1, ids id-0020..id-0001
	firstPage, cursor := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, "", 5, true)
	if len(firstPage) != 5 || firstPage[4].id != "id-0016" {
		t.Fatalf("first page = %+v", firstPage)
	}

	// Simulate 5 new items landing ahead of everything (newest-first) —
	// exactly what a live purchases/redemption collection does between
	// two page requests.
	grown := make([]keysetItem, 0, 25)
	for i := 25; i >= 21; i-- {
		grown = append(grown, keysetItem{sortValue: int64(i), id: fmt.Sprintf("id-%04d", i)})
	}
	grown = append(grown, items...)

	secondPage, _ := KeysetPage(grown, keysetItem.SortValue, keysetItem.ID, cursor, 5, true)
	if len(secondPage) != 5 {
		t.Fatalf("second page len = %d, want 5", len(secondPage))
	}
	// The cursor named "strictly after id-0016" — the new items (21-25)
	// all sort ABOVE that cursor, so they must NOT appear in this page;
	// the second page must resume exactly where the first left off.
	want := []string{"id-0015", "id-0014", "id-0013", "id-0012", "id-0011"}
	for i, it := range secondPage {
		if it.id != want[i] {
			t.Fatalf("secondPage[%d] = %s, want %s (a concurrent prepend must not shift an existing cursor's page)", i, it.id, want[i])
		}
	}
}

func TestKeysetPageLastPageHasNoNextCursor(t *testing.T) {
	items := sortedDescItems(5)
	page, next := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, "", 10, true)
	if len(page) != 5 {
		t.Fatalf("page len = %d, want 5", len(page))
	}
	if next != "" {
		t.Fatalf("expected no next cursor once every item fits in one page, got %q", next)
	}
}

// TestKeysetPageAscendingWalksEveryItem covers the desc=false direction
// (asset records/transactions keep their pre-existing
// oldest-first order) — the mirror of
// TestKeysetPageWalksEveryItemExactlyOnceOver200Records.
func TestKeysetPageAscendingWalksEveryItem(t *testing.T) {
	const total = 53
	items := make([]keysetItem, total)
	for i := 0; i < total; i++ {
		items[i] = keysetItem{sortValue: int64(i), id: fmt.Sprintf("id-%04d", i)}
	}

	seen := map[string]bool{}
	var order []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("walk did not terminate")
		}
		page, next := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, cursor, 7, false)
		for _, it := range page {
			if seen[it.id] {
				t.Fatalf("id %s visited twice", it.id)
			}
			seen[it.id] = true
			order = append(order, it.id)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d of %d items", len(seen), total)
	}
	for i := 0; i < total; i++ {
		if order[i] != fmt.Sprintf("id-%04d", i) {
			t.Fatalf("order[%d] = %s, want ascending order", i, order[i])
		}
	}
}

func TestKeysetPageEmptyInput(t *testing.T) {
	page, next := KeysetPage([]keysetItem(nil), keysetItem.SortValue, keysetItem.ID, "", 10, true)
	if len(page) != 0 || next != "" {
		t.Fatalf("page=%v next=%q, want empty", page, next)
	}
}

func TestDecodeKeysetCursorRejectsGarbage(t *testing.T) {
	if _, ok := DecodeKeysetCursor("not-a-valid-cursor"); ok {
		t.Fatal("expected garbage input to fail to decode")
	}
	if _, ok := DecodeKeysetCursor(""); ok {
		t.Fatal("expected an empty cursor to report not-ok (first page)")
	}
}

func TestEncodeDecodeKeysetCursorRoundTrips(t *testing.T) {
	c := KeysetCursor{SortValue: 42, ID: "abc-123"}
	got, ok := DecodeKeysetCursor(EncodeKeysetCursor(c))
	if !ok || got != c {
		t.Fatalf("round trip = %+v ok=%v, want %+v", got, ok, c)
	}
}
