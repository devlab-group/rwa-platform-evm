package api

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// List endpoints use cursor and limit, defaulting to defaultListLimit items and capping
// responses at maxListLimit
const (
	// defaultListLimit is applied when the caller passes no `limit`
	defaultListLimit = 50
	// maxListLimit is the hard ceiling regardless of what `limit` the
	// caller requests
	maxListLimit = 200
)

// paginationParams parses limit and a decimal-offset cursor for fetch-then-slice
// endpoints. Offsets remain stable while the sorted data is unchanged. Invalid values
// use defaults.
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

// setPaginationHeaders accompanies plain-array list responses. Clients follow
// X-Next-Cursor until absent. A negative total omits X-Total-Count because the count is
// unavailable.
func setPaginationHeaders(c *gin.Context, total, returned int, nextCursor string) {
	if total >= 0 {
		c.Header("X-Total-Count", strconv.Itoa(total))
	}
	c.Header("X-Page-Size", strconv.Itoa(returned))
	if nextCursor != "" {
		c.Header("X-Next-Cursor", nextCursor)
	}
}

// cursorLimitParams parses limit and passes cursor unchanged as a repository keyset
// token for purchases, redemptions, wallets, asset records and transactions.
// Fetch-then-slice endpoints use paginationParams.
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

// maxAuditLogFetch caps listAuditLogs' offset+limit fetch because the repository
// supports a limit but no offset
const maxAuditLogFetch = 2000
