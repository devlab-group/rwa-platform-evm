package api

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// List endpoints cap each response at maxListLimit items, windowed by the
// `cursor` and `limit` query parameters. A caller that sends neither gets the
// first defaultListLimit items, never the whole collection.
const (
	// defaultListLimit is applied when the caller passes no `limit`
	defaultListLimit = 50
	// maxListLimit is the hard ceiling regardless of what `limit` the
	// caller requests
	maxListLimit = 200
)

// paginationParams parses the `limit` and `cursor` query parameters of the
// fetch-then-slice list endpoints. cursor is a decimal offset into the list's
// fixed sort order, so the same cursor names the same boundary across requests
// while the underlying data is unchanged. Invalid values fall back to the
// defaults.
func paginationParams(c *gin.Context) (offset, limit int) {
	limit = defaultListLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if v := c.Query("cursor"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return offset, limit
}

// paginateWindow clamps [offset, offset+limit) to a collection of size total
// and returns the nextCursor for the following page, empty on the last page
func paginateWindow(total, offset, limit int) (start, end int, nextCursor string) {
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	start = offset
	end = start + limit
	if end > total {
		end = total
	}
	if end < total {
		nextCursor = strconv.Itoa(end)
	}
	return start, end, nextCursor
}

// setPaginationHeaders writes the pagination headers shared by every list
// endpoint. The body is a plain JSON array; a client that wants every page
// follows X-Next-Cursor until it is absent. A negative total means the count is
// not available, and X-Total-Count is omitted.
func setPaginationHeaders(c *gin.Context, total, returned int, nextCursor string) {
	if total >= 0 {
		c.Header("X-Total-Count", strconv.Itoa(total))
	}
	c.Header("X-Page-Size", strconv.Itoa(returned))
	if nextCursor != "" {
		c.Header("X-Next-Cursor", nextCursor)
	}
}

// cursorLimitParams parses `limit` like paginationParams but passes `cursor`
// through as an opaque keyset token for a repository ListPage call. It serves
// the endpoints backed by ListPage (purchases, redemptions, wallets, asset
// records, transactions), whose newest-first data changes between requests;
// the fetch-then-slice endpoints use paginationParams.
func cursorLimitParams(c *gin.Context) (cursor string, limit int) {
	limit = defaultListLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return c.Query("cursor"), limit
}

// maxAuditLogFetch caps listAuditLogs' most-recent-N fetch. The audit log
// repository's List takes only a limit and no offset, so the handler fetches
// offset+limit entries, capped here.
const maxAuditLogFetch = 2000
