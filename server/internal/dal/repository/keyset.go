package repository

import (
	"encoding/base64"
	"encoding/json"
)

// KeysetCursor is an opaque keyset-pagination cursor. It names the last item of the
// previous page by its sort value and id, so the next page is the items strictly
// after that one, and no rows are counted and skipped. Pagination therefore stays
// correct, and costs one page of work, while the indexer and reconcilers insert
// into the collection. An offset cursor would skip or repeat rows when that
// happens.
//
// SortValue is an int64: a block number, a unix-second timestamp, or the UnixNano
// of a time.Time field. ID is the item's id and breaks ties between items with the
// same SortValue, such as two purchases in one block.
type KeysetCursor struct {
	SortValue int64  `json:"v"`
	ID        string `json:"id"`
}

// EncodeKeysetCursor renders c as the opaque string a ListPage response carries in
// its X-Next-Cursor header. Clients pass it back unchanged as the cursor query
// parameter and do not build or parse one.
func EncodeKeysetCursor(c KeysetCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return "" // c is always a plain struct of a string and an int64; never actually fails
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeKeysetCursor is the inverse of EncodeKeysetCursor. ok is false for an
// empty or malformed cursor, which callers treat as no cursor (the first
// page) and not as an error
func DecodeKeysetCursor(s string) (KeysetCursor, bool) {
	if s == "" {
		return KeysetCursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return KeysetCursor{}, false
	}
	var c KeysetCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return KeysetCursor{}, false
	}
	return c, true
}

// KeysetPage returns up to limit items from items that come strictly after cursor,
// and a cursor for the next page when more remain. items must already be sorted by
// (sortValue, id) in the direction desc gives: descending for the newest-first
// collections (purchases, redemption requests) and ascending for asset records and
// transactions. The in-memory repositories use it. The MongoDB repositories push
// the equivalent filter, sort and limit into the query and walk the same order.
func KeysetPage[T any](items []T, sortValue func(T) int64, id func(T) string, cursor string, limit int, desc bool) (page []T, next string) {
	start := 0
	if c, ok := DecodeKeysetCursor(cursor); ok {
		start = len(items)
		for i, it := range items {
			sv, iid := sortValue(it), id(it)
			var past bool
			if desc {
				past = sv < c.SortValue || (sv == c.SortValue && iid < c.ID)
			} else {
				past = sv > c.SortValue || (sv == c.SortValue && iid > c.ID)
			}
			if past {
				start = i
				break
			}
		}
	}
	end := start + limit
	if limit <= 0 || end > len(items) {
		end = len(items)
	}
	if start > len(items) {
		start = len(items)
	}
	page = items[start:end]
	if end < len(items) {
		last := items[end-1]
		next = EncodeKeysetCursor(KeysetCursor{SortValue: sortValue(last), ID: id(last)})
	}
	return page, next
}
