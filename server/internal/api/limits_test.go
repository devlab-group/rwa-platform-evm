package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOversizedBodyRejected404s exercises the request-size limit
// (auth.MaxRequestBody, wired router-wide in NewRouter) end to end: a body
// larger than App.MaxRequestBodyBytes must be rejected with 413, not
// silently truncated or accepted, on a route that reads the raw body
// itself (profile/validate) as well as one that binds JSON into a struct
// (compliance/challenge).
func TestOversizedBodyRejected(t *testing.T) {
	env := setupTestApp(t)
	env.app.MaxRequestBodyBytes = 16 // tiny, so the test doesn't need to send megabytes
	router := NewRouter(env.app)

	cases := []struct {
		path string
		body []byte
	}{
		// Raw io.ReadAll body (validateProfile): any bytes past the limit trip it.
		{"/api/v1/profile/validate", bytes.Repeat([]byte("x"), 1024)},
		// c.ShouldBindJSON (createChallenge): must be valid-so-far JSON so the
		// decoder keeps reading (and so hits the MaxBytesReader cutoff) instead
		// of failing on a syntax error before ever reading enough bytes to
		// notice the size limit.
		{"/api/v1/compliance/challenge", append(append([]byte(`{"address":"`), bytes.Repeat([]byte("x"), 1024)...), []byte(`"}`)...)},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status = %d, want 413; body=%s", tc.path, w.Code, w.Body.String())
		}
	}
}

// TestKYCWebhookOversizedBodyRejectedBeforeHMAC specifically covers the
// webhook path called out in review: kycWebhook reads the whole body via
// io.ReadAll BEFORE computing/checking the HMAC signature (it has to — the
// signature covers the raw body), so an oversized delivery must be
// rejected by the size limit at read time, never reaching HMAC
// verification (which would otherwise do real crypto work on attacker-
// controlled megabytes for every retry of an oversized payload).
func TestKYCWebhookOversizedBodyRejectedBeforeHMAC(t *testing.T) {
	env := setupTestApp(t)
	env.app.MaxRequestBodyBytes = 16
	router := NewRouter(env.app)

	body := append([]byte(`{"eventId":"e1","address":"0x0","status":"`), bytes.Repeat([]byte("x"), 1024)...)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", "deadbeef") // deliberately invalid; must never be reached
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
}

// TestUndersizedBodyStillWorks makes sure the limit doesn't clip legitimate
// small requests.
func TestUndersizedBodyStillWorks(t *testing.T) {
	env := setupTestApp(t)
	env.app.MaxRequestBodyBytes = 1 << 20 // 1 MiB, comfortably larger than goldGramProfile
	router := NewRouter(env.app)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile/validate", strings.NewReader(goldGramProfile))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurityHeadersPresent checks the baseline security headers land
// on every response, including a 404 (NoRoute) and a normal 200.
func TestSecurityHeadersPresent(t *testing.T) {
	env := setupTestApp(t)
	for _, path := range []string{"/healthz", "/this-path-does-not-exist"} {
		w := doJSON(t, env.router, http.MethodGet, path, nil, nil)
		for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
			if w.Header().Get(header) == "" {
				t.Errorf("%s: missing security header %s", path, header)
			}
		}
	}
}
