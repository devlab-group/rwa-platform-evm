package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxRequestBody installs http.MaxBytesReader to bound buffered request bodies. It
// reads nothing itself; routes that ignore bodies do no extra reading. Reads past
// maxBytes return *http.MaxBytesError, mapped to 413 by handlers and Idempotency.
// maxBytes <= 0 disables the limit.
func MaxRequestBody(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// ContentSecurityPolicy permits neither unsafe-inline nor unsafe-eval. connect-src
// 'self' suffices: the admin SPA's viem custom(provider) transport uses injected
// window.ethereum, without direct RPC fetches or provider calls through fetch, XHR or
// WebSocket.
// internal/webui uses SetSecurityHeaders to share this policy between SPA and API
// responses.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests"

// SetSecurityHeaders supplies common response headers to internal/webui's SPA handler
// and the API's SecurityHeaders middleware
func SetSecurityHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY") // frame-ancestors 'none' covers current browsers, this covers older ones
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	// Browsers ignore this over plain HTTP, so it takes effect once TLS is
	// terminated in front of the server
	h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	h.Set("X-XSS-Protection", "0") // deprecated, 0 turns off the legacy browser XSS filter
}

// SecurityHeaders is SetSecurityHeaders as Gin middleware for the API router
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		SetSecurityHeaders(c.Writer.Header())
		c.Next()
	}
}
