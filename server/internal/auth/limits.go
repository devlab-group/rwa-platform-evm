package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxRequestBody limits every request body to maxBytes with
// http.MaxBytesReader, so endpoints that buffer the whole body, such as
// POST /api/v1/profile/validate and POST /api/v1/assets/records, cannot be
// used to exhaust memory. The middleware only installs the reader. A read past
// maxBytes returns an *http.MaxBytesError, which the handlers and Idempotency
// map to 413, and a route that never reads its body pays nothing. maxBytes <= 0
// disables the limit.
func MaxRequestBody(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// ContentSecurityPolicy is the policy the admin SPA is built to satisfy: it
// needs neither unsafe-inline nor unsafe-eval. connect-src 'self' is enough
// even though the app talks to wallets, because requests to the injected
// window.ethereum provider are not fetch, XHR or WebSocket traffic, and the
// app's viem transport is custom(provider), so it makes no direct RPC fetch.
//
// internal/webui applies the same value through SetSecurityHeaders, so SPA and
// API responses share one policy.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests"

// SetSecurityHeaders applies the baseline security response headers to h.
// It is exported so internal/webui's SPA handler can apply the same headers as
// the SecurityHeaders middleware.
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
