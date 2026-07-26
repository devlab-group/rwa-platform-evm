package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDefaultTrustedProxiesIgnoresForwardedFor covers SetTrustedProxies:
// with App.TrustedProxies left at its zero
// value (nil), gin must NOT honor X-Forwarded-For for c.ClientIP() — which
// auth.RateLimit keys on — so a caller can't spoof a fresh IP on every
// request to dodge per-IP rate limiting. Two requests with the same real
// RemoteAddr but different spoofed X-Forwarded-For values must be counted
// against the SAME rate-limit bucket.
func TestDefaultTrustedProxiesIgnoresForwardedFor(t *testing.T) {
	env := setupTestApp(t)
	env.app.RateLimitRPS = 0.001 // effectively "one request, then blocked" for the rest of this test's short lifetime
	env.app.RateLimitBurst = 1
	router := NewRouter(env.app)

	doWithForwardedFor := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "203.0.113.10:12345" // same real peer every time
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	if code := doWithForwardedFor(""); code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", code)
	}
	// A spoofed, different X-Forwarded-For on every subsequent request
	// must NOT reset the rate limit bucket if proxies aren't trusted.
	for i, xff := range []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"} {
		if code := doWithForwardedFor(xff); code != http.StatusTooManyRequests {
			t.Errorf("request %d (X-Forwarded-For: %s) status = %d, want 429 (X-Forwarded-For must be ignored by default)", i, xff, code)
		}
	}
}
