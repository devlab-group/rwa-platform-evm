package repository

import (
	"encoding/base64"
	"encoding/json"
)

// KeysetCursor identifies the previous page's last item by SortValue and ID. Continuing
// strictly beyond that pair avoids offset shifts when items are inserted ahead of it;
// existing sort keys must stay stable.
// SortValue holds a block number, Unix seconds or time.Time.UnixNano; ID breaks ties,
// such as purchases in one block. MongoDB filters and limits queries; memory walks an
// already-sorted slice.
type KeysetCursor struct {
	SortValue int64  `json:"v"`
	ID        string `json:"id"`
}

// EncodeKeysetCursor produces the opaque X-Next-Cursor value clients return unchanged
// as the cursor parameter
func EncodeKeysetCursor(c KeysetCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return "" // c is always a plain struct of a string and an int64; never actually fails
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeKeysetCursor reverses encoding; empty or malformed input returns ok=false,
// treated as the first page rather than an error
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

// KeysetPage slices sorted (sortValue, id) items strictly beyond cursor, using desc for
// direction. A positive limit caps the page; non-positive returns the remainder. next
// is set only when more remain. Memory repositories use this helper; MongoDB pushes
// equivalent filtering into queries.
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
