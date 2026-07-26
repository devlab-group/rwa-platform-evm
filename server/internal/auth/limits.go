package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxRequestBody bounds every request body to maxBytes via
// http.MaxBytesReader. An unbounded request body is a trivial
// memory-exhaustion vector against endpoints that buffer
// the whole request, like POST /api/v1/profile/validate or
// .../assets/records). A body that exceeds the limit fails with 413 the
// first time a handler actually reads past maxBytes (http.MaxBytesReader's
// own behavior — this middleware only installs the reader; it does not
// eagerly read the body itself, so a route that never reads its body pays
// nothing extra). maxBytes<=0 disables the limit entirely.
func MaxRequestBody(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// ContentSecurityPolicy is the CSP the production web build validates
// against: no unsafe-inline/unsafe-eval needed anywhere — the SPA's
// remaining inline styles were removed specifically to make that possible.
// connect-src 'self' is correct as-is
// despite the app talking to wallets: window.ethereum.request is the
// injected-provider's own out-of-page channel, not fetch/XHR/WebSocket, so
// it is not subject to connect-src at all, and viem's custom(provider)
// transport means there is never a raw RPC fetch() to allow either.
//
// This is the single source of truth for the string — internal/webui
// reuses it via SetSecurityHeaders rather than keeping its own copy, so
// the SPA and API responses can never drift apart on this.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests"

// SetSecurityHeaders applies the baseline security response headers to h.
// Exported (not just used internally by the SecurityHeaders middleware
// below) so internal/webui's SPA-serving NoRoute handler can apply the
// exact same headers without duplicating this list.
func SetSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY") // redundant with the CSP's frame-ancestors 'none' for browsers that honor both; kept for older UAs that only understand this header
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	// Meaningless over plain HTTP (no-op), harmless to always send — takes
	// effect the moment a deployment terminates TLS in front of this
	// server, without needing a code change then.
	h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	h.Set("X-XSS-Protection", "0") // deprecated legacy header; explicitly disabled per modern guidance, not left unset/ambiguous
}

// SecurityHeaders is SetSecurityHeaders as Gin middleware, for the API
// router's chain (internal/api.NewRouter).
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		SetSecurityHeaders(c.Writer.Header())
		c.Next()
	}
}
