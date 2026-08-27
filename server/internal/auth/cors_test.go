package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const (
	fxAllowedOrigin = "http://localhost:5137"
	fxOtherOrigin   = "http://evil.example"
)

// corsRouter builds a minimal router with the CORS middleware in front of a
// handler that records whether it ran — several of the assertions below are
// about whether the chain CONTINUED, not just which headers came back.
func corsRouter(t *testing.T, origins []string, reached *bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS(origins))
	h := func(c *gin.Context) {
		*reached = true
		c.String(http.StatusOK, "ok")
	}
	r.GET("/api/v1/project", h)
	r.POST("/api/v1/project", h)
	return r
}

func do(r *gin.Engine, method, path, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCORSAllowsListedOrigin(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin}, &reached)

	w := do(r, http.MethodGet, "/api/v1/project", fxAllowedOrigin)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != fxAllowedOrigin {
		t.Errorf("Allow-Origin = %q, want %q", got, fxAllowedOrigin)
	}
	if !reached {
		t.Error("handler should still run for an allowed simple request")
	}
}

// TestCORSEchoesNeverWildcards: the header must carry the request's exact
// origin, never "*". A wildcard alongside an Authorization bearer is the whole
// reason config.Load refuses "*" — the middleware must not reintroduce it.
func TestCORSEchoesNeverWildcards(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin, "https://investor.example"}, &reached)

	w := do(r, http.MethodGet, "/api/v1/project", "https://investor.example")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://investor.example" {
		t.Errorf("Allow-Origin = %q, want the exact requesting origin", got)
	}
	if strings.Contains(w.Header().Get("Access-Control-Allow-Origin"), "*") {
		t.Error("Allow-Origin must never be a wildcard")
	}
}

// TestCORSUnlistedOriginGetsNoHeaders: an unlisted origin gets NO CORS headers,
// which is what makes the browser block it. The request itself still runs —
// CORS is browser-enforced, and rejecting server-side would change behaviour for
// non-browser clients that are not subject to it at all.
func TestCORSUnlistedOriginGetsNoHeaders(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin}, &reached)

	w := do(r, http.MethodGet, "/api/v1/project", fxOtherOrigin)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty for an unlisted origin", got)
	}
	if !reached {
		t.Error("an unlisted origin must not be blocked server-side")
	}
}

// TestCORSPreflightIsAnsweredAndStops is the case that motivated putting this
// middleware ahead of Authenticate: a preflight carries no Authorization header
// by design, so if it continued down the chain it would be rejected as
// unauthenticated and the real request would never be attempted.
func TestCORSPreflightIsAnsweredAndStops(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin}, &reached)

	w := do(r, http.MethodOptions, "/api/v1/project", fxAllowedOrigin)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	if reached {
		t.Error("preflight must be terminated by the middleware, not passed to a handler")
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin": fxAllowedOrigin,
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	// Every header a real request may send must be listed, or the browser
	// fails the preflight and the call never happens.
	allowHeaders := w.Header().Get("Access-Control-Allow-Headers")
	for _, h := range []string{"Authorization", "Content-Type", "Idempotency-Key", WalletSessionHeader} {
		if !strings.Contains(allowHeaders, h) {
			t.Errorf("Allow-Headers %q is missing %q", allowHeaders, h)
		}
	}
	allowMethods := w.Header().Get("Access-Control-Allow-Methods")
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if !strings.Contains(allowMethods, m) {
			t.Errorf("Allow-Methods %q is missing %q", allowMethods, m)
		}
	}
	if w.Header().Get("Access-Control-Max-Age") == "" {
		t.Error("Max-Age should be set so a burst of calls doesn't re-preflight each time")
	}
}

// TestCORSPreflightFromUnlistedOriginIsNotAnswered: an unlisted origin must not
// get a 204 with CORS headers — otherwise the allowlist would be decorative.
func TestCORSPreflightFromUnlistedOriginIsNotAnswered(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin}, &reached)

	w := do(r, http.MethodOptions, "/api/v1/project", fxOtherOrigin)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty", got)
	}
	if w.Code == http.StatusNoContent {
		t.Error("an unlisted origin's preflight must not be answered with 204")
	}
}

// TestCORSNoCredentialsHeader: this API authenticates with Authorization /
// X-Wallet-Session headers, never cookies, so credentialed mode is neither
// needed nor safe to enable alongside an echoed origin.
func TestCORSNoCredentialsHeader(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin}, &reached)

	w := do(r, http.MethodGet, "/api/v1/project", fxAllowedOrigin)
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Allow-Credentials = %q, want it unset", got)
	}
}

// TestCORSSetsVaryOnEveryResponse: the response now depends on Origin, so a
// shared cache that ignored that could serve one origin the headers computed
// for another. It must be present even when the origin does NOT match.
func TestCORSSetsVaryOnEveryResponse(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{fxAllowedOrigin}, &reached)

	for _, origin := range []string{fxAllowedOrigin, fxOtherOrigin, ""} {
		w := do(r, http.MethodGet, "/api/v1/project", origin)
		if !strings.Contains(w.Header().Get("Vary"), "Origin") {
			t.Errorf("origin %q: Vary = %q, want it to include Origin", origin, w.Header().Get("Vary"))
		}
	}
}

// TestCORSDisabledByDefault: with no origins configured the middleware is inert
// — no headers, and a preflight is left to the router (404/405). That is the
// correct posture for a deployment serving only the embedded, same-origin
// console.
func TestCORSDisabledByDefault(t *testing.T) {
	reached := false
	r := corsRouter(t, nil, &reached)

	w := do(r, http.MethodGet, "/api/v1/project", fxAllowedOrigin)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty when CORS is disabled", got)
	}
	if got := w.Header().Get("Vary"); strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want no Origin when CORS is disabled", got)
	}
	if !reached {
		t.Error("disabling CORS must not affect ordinary request handling")
	}
}

// TestCORSIgnoresEmptyEntries: a YAML list with a stray blank entry must not
// make the empty string a matchable origin.
func TestCORSIgnoresEmptyEntries(t *testing.T) {
	reached := false
	r := corsRouter(t, []string{"", "   ", fxAllowedOrigin}, &reached)

	w := do(r, http.MethodGet, "/api/v1/project", "")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want empty for a request with no Origin", got)
	}
	if w2 := do(r, http.MethodGet, "/api/v1/project", fxAllowedOrigin); w2.Header().Get("Access-Control-Allow-Origin") != fxAllowedOrigin {
		t.Error("a real origin alongside blank entries must still match")
	}
}
