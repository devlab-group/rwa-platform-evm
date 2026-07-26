package repository

import (
	"encoding/base64"
	"encoding/json"
)

// KeysetCursor is an opaque compound keyset-pagination cursor. It uses
// deterministic compound keys and bounded Mongo queries, avoiding offset
// cursors for changing, newest-first data. It names the last item of the
// PREVIOUS
// page by that item's own sort-key value plus its id as a tiebreaker, so
// the next page's query is "strictly past this specific item," never
// "skip N rows" — it stays correct and bounded to one page's worth of
// work even while the collection is being inserted into concurrently by
// the indexer/reconcilers (an offset cursor silently skips or repeats
// rows under exactly that condition, since "row 50" names a different
// record once anything shifts ahead of it).
//
// SortValue is a decimal int64 because every keyset-paginated collection
// in this codebase sorts by either a block number or a unix-second
// timestamp — both fit int64 safely. ID is that item's own id, used only
// to break ties when two items share the same SortValue (e.g. two
// purchases in the same block).
type KeysetCursor struct {
	SortValue int64  `json:"v"`
	ID        string `json:"id"`
}

// EncodeKeysetCursor renders c as the opaque string a List/ListPage
// response's X-Next-Cursor header carries — callers must treat the result
// as opaque (round-trip it verbatim as the next request's `cursor` query
// parameter), never construct or parse one themselves; that contract is
// what lets the encoding here change without breaking a compliant client
// (api/pagination.go's existing doc comment already establishes this for
// the offset-based cursors other list endpoints still use).
func EncodeKeysetCursor(c KeysetCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return "" // c is always a plain struct of a string and an int64; never actually fails
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeKeysetCursor is EncodeKeysetCursor's inverse. ok is false for an
// empty, malformed, or tampered cursor — callers treat that identically to
// "no cursor" (the first page), never as an error: a client is never
// expected to construct one of these itself, but nothing here trusts that.
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

// KeysetPage windows items — which callers MUST already have sorted by
// (sortValue, id) in the SAME direction as desc, the exact order every
// ListPage implementation in this codebase sorts by (desc=true for
// newest-first collections like purchases/redemption requests; desc=false
// for the oldest-first ones — asset records, transactions — that predate
// keyset pagination and keep their existing order) — to the page strictly
// after cursor, up to limit items. It backs
// the in-memory repository implementations (repository/memory), which
// hold everything in one slice already; the mongodb implementations
// instead push the equivalent $or/$lt-or-$gt filter and sort/limit
// directly into the query, so they never materialize more than one page,
// but both walk the identical logical order this function defines — see
// the package doc comment on KeysetCursor for why (sortValue, id) rather
// than an offset.
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
