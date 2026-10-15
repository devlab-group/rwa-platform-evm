package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// corsAllowedHeaders permits Content-Type, the admin JWT's Authorization header, the
// investor's X-Wallet-Session and admin write routes' Idempotency-Key in browser
// preflights
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

// corsMaxAge caches preflight results for ten minutes, reducing repeated preflights
// while bounding the delay before origin-list changes take effect
const corsMaxAge = 10 * time.Minute

// CORS permits browser access from allowedOrigins, used by the standalone investor SPA;
// the embedded admin console is same-origin. An empty list emits no CORS headers and
// passes OPTIONS to the router, which has no OPTIONS routes.
// With a configured list, every response varies by Origin. Matching origins are echoed
// in Access-Control-Allow-Origin; their preflights return 204 before auth and rate
// limiting. Unmatched requests still run, but browsers block response access.
// Non-browser clients are unaffected.
// Configuration rejects "*". Access-Control-Allow-Credentials is omitted because
// authentication uses Authorization and X-Wallet-Session headers, not cookies.
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
