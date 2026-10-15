package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOversizedBodyRejected404s checks router-wide auth.MaxRequestBody: exceeding
// App.MaxRequestBodyBytes returns 413 on both raw-body profile/validate and
// JSON-binding compliance/challenge routes, rather than truncating or accepting the
// body
func TestOversizedBodyRejected(t *testing.T) {
	env := setupTestApp(t)
	env.app.MaxRequestBodyBytes = 16 // tiny, so the test doesn't need to send megabytes
	router := NewRouter(env.app)

	cases := []struct {
		path string
		body []byte
	}{
		// Raw io.ReadAll body (validateProfile): any bytes past the limit trip it
		{"/api/v1/profile/validate", bytes.Repeat([]byte("x"), 1024)},
		// Keep JSON valid until the size limit so c.ShouldBindJSON reaches it before any
		// syntax error
		{"/api/v1/compliance/challenge", append(append([]byte(`{"address":"`), bytes.Repeat([]byte("x"), 1024)...), []byte(`"}`)...)},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", env.bearer)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status = %d, want 413; body=%s", tc.path, w.Code, w.Body.String())
		}
	}
}

// TestKYCWebhookOversizedBodyRejectedBeforeHMAC checks that raw-body reading rejects an
// oversized delivery before HMAC verification
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

// TestUndersizedBodyStillWorks checks that the limit does not clip small requests
func TestUndersizedBodyStillWorks(t *testing.T) {
	env := setupTestApp(t)
	env.app.MaxRequestBodyBytes = 1 << 20 // 1 MiB, comfortably larger than goldGramProfile
	router := NewRouter(env.app)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile/validate", strings.NewReader(goldGramProfile))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", env.bearer)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

// TestSecurityHeadersPresent checks that the baseline security headers appear on
// every response, including a 404 (NoRoute) and a normal 200
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
