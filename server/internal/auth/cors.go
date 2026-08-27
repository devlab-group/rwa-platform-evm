package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// corsAllowedHeaders is every request header a browser client may send.
// Authorization carries the admin JWT, X-Wallet-Session the investor session,
// and Idempotency-Key is required on state-changing calls — omitting any one of
// them makes the corresponding request fail preflight while looking like a
// server bug rather than a CORS one.
var corsAllowedHeaders = strings.Join([]string{
	"Authorization",
	"Content-Type",
	"Idempotency-Key",
	WalletSessionHeader,
}, ", ")

// corsAllowedMethods covers every method the API routes actually use, plus
// OPTIONS for the preflight itself.
var corsAllowedMethods = strings.Join([]string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
}, ", ")

// corsMaxAge is how long a browser may cache a preflight result. Ten minutes
// keeps an origin-list change from lingering for hours while still collapsing
// the preflight for a burst of calls.
const corsMaxAge = 10 * time.Minute

// CORS answers cross-origin requests for the origins in allowedOrigins, and
// only those.
//
// The admin console does NOT need this: it is embedded in this binary and
// served same-origin. The standalone investor SPA (investor-web/) does — it is
// a separate deployable that talks to this server's public + X-Wallet-Session
// API from its own origin, which is the whole reason the setting exists.
//
// An empty list disables CORS entirely (no headers emitted, preflights fall
// through to the router's own 404/405): same-origin only, which is the correct
// default for a deployment that serves just the embedded console.
//
// Deliberate properties:
//
//   - The Origin is ECHOED, never reflected blindly and never "*". A request
//     from an unlisted origin gets no CORS headers at all, so the browser
//     blocks it — the server does not need to (and must not) reject the request
//     itself, since CORS is a browser-enforced policy and a non-browser client
//     is unaffected either way.
//   - "*" is rejected at config load, not here (see config.Load): with
//     Authorization allowed, a wildcard would let any site on the internet
//     drive an authenticated admin session from a victim's browser.
//   - Access-Control-Allow-Credentials is NOT sent. This API authenticates with
//     Authorization/X-Wallet-Session headers, never cookies, so credentialed
//     mode buys nothing and would forbid the wildcard-free echo pattern from
//     ever being loosened safely.
//   - Vary: Origin is always set when a list is configured, including on
//     non-matching origins, so a shared cache can never serve one origin's
//     CORS headers to another.
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
		// Vary goes on every response once CORS is configured, matched or not:
		// the response body/headers now depend on Origin, and a cache that
		// doesn't know that could hand origin A the headers computed for B.
		c.Writer.Header().Add("Vary", "Origin")

		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next() // same-origin or a non-browser client
			return
		}
		if _, ok := allowed[origin]; !ok {
			// Unlisted: emit nothing. The browser blocks the response; a
			// non-browser caller is unaffected, exactly as CORS intends.
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)

		if c.Request.Method == http.MethodOptions {
			// Preflight: answer here and stop. It must NOT continue down the
			// chain — Authenticate would reject it (a preflight carries no
			// Authorization header, by design), and the router has no OPTIONS
			// route to match anyway.
			h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			h.Set("Access-Control-Max-Age", maxAge)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
