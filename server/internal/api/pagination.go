package api

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// Without a cap, many authenticated list handlers would fetch an entire Mongo
// collection and serialize it in one response (audit logs in particular could
// request limit 0, i.e. no limit). The bound is applied at the API-HANDLER
// layer — cap what's serialized back to the client to a documented hard
// maximum, windowed by an opaque `cursor`/`limit` query pair — rather than
// widening every repository interface's List signature. A caller that omits
// both query parameters still gets the bounded default, not the full
// collection: the server-side limit holds even when the client sends nothing.
const (
	// defaultListLimit is applied when the caller passes no `limit`.
	defaultListLimit = 50
	// maxListLimit is the hard ceiling regardless of what `limit` the
	// caller requests.
	maxListLimit = 200
)

// paginationParams parses the `limit`/`cursor` query parameters shared by
// the fetch-then-slice list endpoints. cursor is an opaque decimal offset into the
// list's STABLE ordering (every repository List call already sorts by a
// fixed key — createdAt/receivedAt/etc. — so the same cursor value always
// names the same boundary across requests as long as the underlying data
// doesn't change).
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

// paginateWindow bounds [offset, offset+limit) to a valid slice range over
// a collection of size total, returning the nextCursor a client should pass
// to fetch the following page (empty once there is no further page).
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

// setPaginationHeaders writes the pagination response headers every
// list endpoint shares. The response BODY stays a plain JSON array (the
// pre-existing api/openapi.yaml response shape for every one of these
// endpoints is unchanged) so this is purely additive for existing clients
// that ignore the new headers; a client that wants every page follows
// X-Next-Cursor until it is absent. total < 0 means "not cheaply knowable"
// (listAuditLogs: the repository only exposes a most-recent-N query, not a
// true collection count) — X-Total-Count is omitted rather than sent wrong.
func setPaginationHeaders(c *gin.Context, total, returned int, nextCursor string) {
	if total >= 0 {
		c.Header("X-Total-Count", strconv.Itoa(total))
	}
	c.Header("X-Page-Size", strconv.Itoa(returned))
	if nextCursor != "" {
		c.Header("X-Next-Cursor", nextCursor)
	}
}

// cursorLimitParams parses `limit` exactly like paginationParams, but
// treats `cursor` as an OPAQUE keyset token passed straight through to a
// repository ListPage call — never decoded/interpreted here, and never a
// decimal offset. Keyset pagination avoids offset cursors for changing,
// newest-first data (see repository.KeysetCursor's doc comment for why an
// offset is unsafe for exactly this kind of collection). Used by every list
// endpoint
// backed by a ListPage repository method (purchases, redemptions,
// wallets, asset records, transactions) instead of paginationParams/
// paginateWindow above, which the remaining fetch-then-slice endpoints
// still use.
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

// maxAuditLogFetch bounds listAuditLogs' underlying most-recent-N fetch.
// AuditLogRepository.List(category, limit) has no skip/offset parameter, only a
// limit, so we pass a hard cap instead of widening its signature. offset+limit
// is capped at this so a client requesting a far-out page cursor can't force an
// effectively-unbounded single fetch.
const maxAuditLogFetch = 2000
