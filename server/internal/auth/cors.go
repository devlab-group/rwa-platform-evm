package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// corsAllowedHeaders lists the request headers a browser client may send.
// Authorization carries the admin JWT, X-Wallet-Session the investor session,
// and Idempotency-Key is required by the idempotent admin routes. A header
// missing from this list fails the preflight, which looks like a server bug
// rather than a CORS error.
var corsAllowedHeaders = strings.Join([]string{
	"Authorization",
	"Content-Type",
	"Idempotency-Key",
	WalletSessionHeader,
}, ", ")

// corsAllowedMethods lists the methods a cross-origin client may use, plus
// OPTIONS for the preflight itself
var corsAllowedMethods = strings.Join([]string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
}, ", ")

// corsMaxAge is how long a browser may cache a preflight result. Ten minutes
// lets an origin-list change take effect within the hour while still sparing a
// burst of calls a preflight each.
const corsMaxAge = 10 * time.Minute

// CORS permits browser access from allowedOrigins. The embedded admin console
// is same-origin; the standalone investor SPA needs this allowlist for public
// and X-Wallet-Session routes.
//
// An empty list emits no CORS headers and lets preflight requests reach the
// router, which has no OPTIONS routes. With a configured list:
//   - A matching Origin is echoed in Access-Control-Allow-Origin. An unmatched
//     origin gets no CORS headers; the browser blocks access to the response,
//     while the server still handles the request. Non-browser clients are unaffected.
//   - Configuration rejects "*" to avoid exposing authenticated API responses
//     to arbitrary origins.
//   - Access-Control-Allow-Credentials is omitted because authentication uses
//     Authorization and X-Wallet-Session headers, not cookies.
//   - Vary: Origin appears on matched and unmatched responses to prevent a
//     shared cache from mixing origins' CORS headers.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return func(c *gin.Context) { c.Next() }
	}

	maxAge := strconv.Itoa(int(corsMaxAge.Seconds()))
	return func(c *gin.Context) {
		// The response headers depend on Origin from here on, so every response
		// carries Vary, matched or not
		c.Writer.Header().Add("Vary", "Origin")

		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next() // same-origin or a non-browser client
			return
		}
		if _, ok := allowed[origin]; !ok {
			// Unlisted: emit nothing, the browser blocks the response
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)

		if c.Request.Method == http.MethodOptions {
			// Answer the preflight here. Browsers send no Authorization header on
			// it, and the router has no OPTIONS routes
			h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			h.Set("Access-Control-Max-Age", maxAge)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
