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
	// This fixture sorts by sortValue descending, breaking ties by ID descending
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

// TestKeysetPageWalksEveryItemExactlyOnceOver200Records checks a sorted walk through
// more than 200 items without gaps or duplicate visits
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

// TestKeysetPageStableUnderConcurrentPrepend requires newest-first inserts ahead of the
// cursor to leave existing pages unchanged, without skipped or duplicated records.
// Offset cursors would shift.
func TestKeysetPageStableUnderConcurrentPrepend(t *testing.T) {
	items := sortedDescItems(20) // sortValue 20..1, ids id-0020..id-0001
	firstPage, cursor := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, "", 5, true)
	if len(firstPage) != 5 || firstPage[4].id != "id-0016" {
		t.Fatalf("first page = %+v", firstPage)
	}

	// 5 new items land ahead of everything, as new purchases and redemptions do
	// between two page requests
	grown := make([]keysetItem, 0, 25)
	for i := 25; i >= 21; i-- {
		grown = append(grown, keysetItem{sortValue: int64(i), id: fmt.Sprintf("id-%04d", i)})
	}
	grown = append(grown, items...)

	secondPage, _ := KeysetPage(grown, keysetItem.SortValue, keysetItem.ID, cursor, 5, true)
	if len(secondPage) != 5 {
		t.Fatalf("second page len = %d, want 5", len(secondPage))
	}
	// After id-0016, resume the existing walk; newly inserted items 21..25 sort ahead of
	// the cursor
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

// TestKeysetPageAscendingWalksEveryItem is the desc=false counterpart of
// TestKeysetPageWalksEveryItemExactlyOnceOver200Records, for the oldest-first
// collections (asset records, transactions)
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

// TestKeysetPageTiesBreakOnID requires cursors to pass the last (value, ID) pair in either
// direction without skipping or repeating tied items
func TestKeysetPageTiesBreakOnID(t *testing.T) {
	for _, desc := range []bool{true, false} {
		items := []keysetItem{{5, "a"}, {5, "b"}, {5, "c"}, {5, "d"}, {5, "e"}}
		want := "abcde"
		if desc {
			items = []keysetItem{{5, "e"}, {5, "d"}, {5, "c"}, {5, "b"}, {5, "a"}}
			want = "edcba"
		}
		var got string
		cursor := ""
		for range 5 {
			page, next := KeysetPage(items, keysetItem.SortValue, keysetItem.ID, cursor, 2, desc)
			for _, it := range page {
				got += it.id
			}
			if next == "" {
				break
			}
			cursor = next
		}
		if got != want {
			t.Errorf("desc=%v: walk = %q, want %q", desc, got, want)
		}
	}
}
