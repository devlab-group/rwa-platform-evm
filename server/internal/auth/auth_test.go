package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/repository"
)

func init() { gin.SetMode(gin.TestMode) }

// TestContentSecurityPolicyMatchesWebEngValidatedString pins the exact CSP
// string web-eng validated against the real production build (wallet flows
// exercised, no unsafe-inline/unsafe-eval needed — see web/CSP-AUDIT.md) so
// an edit here can't silently drift from what was actually tested against
// the SPA.
func TestContentSecurityPolicyMatchesWebEngValidatedString(t *testing.T) {
	want := "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; upgrade-insecure-requests"
	if ContentSecurityPolicy != want {
		t.Errorf("ContentSecurityPolicy =\n%q\nwant\n%q", ContentSecurityPolicy, want)
	}
}

func TestSetSecurityHeadersAppliesFullBaseline(t *testing.T) {
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	for header, want := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Content-Security-Policy":   ContentSecurityPolicy,
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
		"X-XSS-Protection":          "0",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// testJWTSecret is a >=32-byte HMAC key for the auth-middleware tests.
var testJWTSecret = []byte("auth-package-test-jwt-secret-32bytes!!")

func TestAuthenticateAndRequireRole(t *testing.T) {
	const adminAddr = "0x1111111111111111111111111111111111111111"
	token, _, err := IssueAdminJWT(testJWTSecret, adminAddr, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(Authenticate(testJWTSecret))
	r.POST("/admin-only", RequireRole(RoleAdmin), func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/public", func(c *gin.Context) { c.Status(http.StatusOK) })

	// No token: forbidden on admin route.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin-only", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("no token: status = %d, want 403", w.Code)
	}

	// Invalid bearer: still forbidden (falls through to readonly).
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin-only", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("invalid bearer: status = %d, want 403", w.Code)
	}

	// Valid admin JWT: allowed, and the principal is the admin address.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin-only", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("with JWT: status = %d, want 200", w.Code)
	}

	// Public route works without any token.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/public", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("public route: status = %d, want 200", w.Code)
	}
}

func TestIdempotencyReplaysIdenticalResponse(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	calls := 0
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) {
		calls++
		c.JSON(http.StatusAccepted, gin.H{"call": calls})
	})

	body := []byte(`{"a":1}`)
	do := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-1")
		r.ServeHTTP(w, req)
		return w
	}

	w1 := do()
	w2 := do()

	if calls != 1 {
		t.Errorf("handler called %d times, want 1", calls)
	}
	if w1.Code != http.StatusAccepted || w2.Code != http.StatusAccepted {
		t.Errorf("status codes: %d, %d", w1.Code, w2.Code)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Errorf("bodies differ: %q vs %q", w1.Body.String(), w2.Body.String())
	}
}

// TestIdempotencyConcurrentIdenticalRequestsExecuteHandlerOnce is the S3
// regression test: N goroutines fire the exact same request (same key,
// same body) at once. Reserve's atomicity must ensure exactly one of them
// runs the handler; every other goroutine either replays that one's
// response or (if it raced in before Complete) gets a 409 "in progress" —
// it must NEVER independently execute the handler's side effect.
func TestIdempotencyConcurrentIdenticalRequestsExecuteHandlerOnce(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls int32
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		// Give other goroutines a real chance to race in while this
		// handler is still running, so the test would actually catch a
		// regression back to the old Get-then-Save race instead of passing
		// by luck on scheduling alone.
		time.Sleep(10 * time.Millisecond)
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	})

	const n = 20
	body := []byte(`{"a":1}`)
	var wg sync.WaitGroup
	codes := make([]int, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader(body))
			req.Header.Set("Idempotency-Key", "concurrent-key")
			r.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler executed %d times, want exactly 1", got)
	}
	accepted := 0
	for _, code := range codes {
		if code == http.StatusAccepted {
			accepted++
		} else if code != http.StatusConflict {
			t.Errorf("unexpected status %d (want 202 or 409)", code)
		}
	}
	if accepted == 0 {
		t.Error("no request observed a 202 — every goroutine lost the race, which shouldn't be possible")
	}
}

func TestIdempotencyRejectsKeyReuseWithDifferentBody(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) { c.JSON(http.StatusAccepted, gin.H{"ok": true}) })

	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader([]byte(`{"a":1}`)))
	req1.Header.Set("Idempotency-Key", "key-2")
	r.ServeHTTP(w1, req1)

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader([]byte(`{"a":2}`)))
	req2.Header.Set("Idempotency-Key", "key-2")
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w2.Code)
	}
}

func TestIdempotencyDoesNotStoreErrorResponses(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	calls := 0
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) {
		calls++
		c.JSON(http.StatusBadRequest, gin.H{"code": "bad"})
	})

	body := []byte(`{"a":1}`)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "key-3")
		r.ServeHTTP(w, req)
	}
	if calls != 2 {
		t.Errorf("handler called %d times, want 2 (errors are not cached)", calls)
	}
}

// TestIdempotencyRequiresHeader pins that the Idempotency-Key header is
// mandatory: if it were optional, every mutation could bypass protection.
// Every route this middleware wires onto is side-effecting, so a missing
// header is rejected outright — the handler must never run at all — with a
// stable machine-readable code, not silently treated as "no dedup requested."
func TestIdempotencyRequiresHeader(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	calls := 0
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) {
		calls++
		c.JSON(http.StatusAccepted, gin.H{"call": calls})
	})

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/deploy", bytes.NewReader([]byte(`{}`)))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("call %d: status = %d, want 400 for a missing Idempotency-Key", i, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "idempotency_key_required" {
			t.Errorf("code = %v, want idempotency_key_required", body["code"])
		}
	}
	if calls != 0 {
		t.Errorf("handler called %d times, want 0 (missing key must reject before the handler ever runs)", calls)
	}
}

// TestIdempotencyScopedByRoute guards against cross-route collisions: if
// records stored method+path but replay checked only the body hash, two
// different routes could collide. Two different routes receiving an identical
// Idempotency-Key and body must each execute independently, not share a
// cached response.
func TestIdempotencyScopedByRoute(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls1, calls2 int
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) { calls1++; c.JSON(http.StatusAccepted, gin.H{"route": "deploy"}) })
	r.POST("/other", func(c *gin.Context) { calls2++; c.JSON(http.StatusAccepted, gin.H{"route": "other"}) })

	body := []byte(`{"a":1}`)
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "same-key-different-route")
		r.ServeHTTP(w, req)
		return w
	}

	w1 := do("/deploy")
	w2 := do("/other")

	if calls1 != 1 || calls2 != 1 {
		t.Errorf("calls1=%d calls2=%d, want each route's handler executed exactly once", calls1, calls2)
	}
	if w1.Body.String() == w2.Body.String() {
		t.Errorf("expected distinct responses per route, got the same body for both: %q", w1.Body.String())
	}
}

// TestIdempotencyScopedByPathParams guards against cross-record collisions:
// if POST /assets/records/A/reissue and .../B/reissue both scoped only to the
// /assets/records/:recordId template, the second would replay the first
// record's response and never reissue B. Two requests to the SAME route
// template but different concrete path parameter values, with the same
// Idempotency-Key and (empty) body, must execute independently.
func TestIdempotencyScopedByPathParams(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls int
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/assets/records/:recordId/reissue", func(c *gin.Context) {
		calls++
		c.JSON(http.StatusOK, gin.H{"recordId": c.Param("recordId")})
	})

	do := func(recordID string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/assets/records/"+recordID+"/reissue", bytes.NewReader(nil))
		req.Header.Set("Idempotency-Key", "same-key-different-record")
		r.ServeHTTP(w, req)
		return w
	}

	wA := do("A")
	wB := do("B")

	if calls != 2 {
		t.Errorf("handler executed %d times, want 2 (different recordIds must not collide on the same route template)", calls)
	}
	if wA.Body.String() == wB.Body.String() {
		t.Errorf("expected distinct per-record responses, got the same body for both: %q", wA.Body.String())
	}
}

// TestIdempotencyScopedByPrincipal guards principal isolation: the key must
// be scoped to the authenticated principal.
// Two different principals hitting the same route with the same key and body
// must not share a cached response — each principal's replay protection is
// independent. The principal is set directly here (a tiny middleware) so the
// guarantee is exercised at the Idempotency layer regardless of which auth
// mechanism populated the principal.
func TestIdempotencyScopedByPrincipal(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls int
	r := gin.New()
	// Read a per-request principal from a test header and stash it exactly
	// where Authenticate would.
	r.Use(func(c *gin.Context) {
		c.Set(contextPrincipalKey, c.GetHeader("X-Test-Principal"))
		c.Next()
	})
	r.Use(Idempotency(store, time.Hour))
	r.POST("/x", func(c *gin.Context) { calls++; c.JSON(http.StatusAccepted, gin.H{"call": calls}) })

	body := []byte(`{"a":1}`)
	do := func(principal string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "shared-key")
		req.Header.Set("X-Test-Principal", principal)
		r.ServeHTTP(w, req)
		return w
	}

	do("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	do("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")

	if calls != 2 {
		t.Errorf("handler called %d times, want 2 (different principals must not share a cached response)", calls)
	}
}

// TestIdempotencyRequireRoleBeforeMiddlewareBlocksUnauthorizedReplay pins
// the middleware ordering: internal/api/api.go runs
// auth.RequireRole BEFORE auth.Idempotency on every idempotent route, so an
// unauthorized caller who guesses/observes a valid Idempotency-Key can
// never reach the cache lookup at all — they are rejected by RequireRole
// first, every time, regardless of what a prior authorized caller cached.
func TestIdempotencyRequireRoleBeforeMiddlewareBlocksUnauthorizedReplay(t *testing.T) {
	const adminAddr = "0x2222222222222222222222222222222222222222"
	token, _, err := IssueAdminJWT(testJWTSecret, adminAddr, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	store := memory.NewIdempotencyRepository()
	r := gin.New()
	r.Use(Authenticate(testJWTSecret))
	// Ordering matches internal/api/api.go: RequireRole, then idem.
	r.POST("/x", RequireRole(RoleAdmin), Idempotency(store, time.Hour), func(c *gin.Context) {
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	})

	body := []byte(`{"a":1}`)
	// Admin executes and caches successfully.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("admin call: status = %d, want 202", w.Code)
	}

	// An unauthenticated replay with the identical key+body must be
	// rejected by RequireRole, never served the admin's cached 202.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req2.Header.Set("Idempotency-Key", "k")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("unauthorized replay: status = %d, want 403 (must never see the cached 2xx)", w2.Code)
	}
}

// TestIdempotencyRejectsOversizedKey pins that oversized keys are bounded.
func TestIdempotencyRejectsOversizedKey(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Idempotency-Key", strings.Repeat("k", maxIdempotencyKeyBytes+1))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an oversized Idempotency-Key", w.Code)
	}
}

// TestIdempotencyExpiredReservationIsTakenOver pins that ExpiresAt is
// actually enforced, not just written. A cached (completed) response
// older than ttl must no longer be replayed — the next identical request
// re-executes the handler instead of being stuck or serving a stale cache
// forever.
func TestIdempotencyExpiredReservationIsTakenOver(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls int32
	r := gin.New()
	r.Use(Idempotency(store, 20*time.Millisecond))
	r.POST("/x", func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	})

	do := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Idempotency-Key", "expiring-key")
		r.ServeHTTP(w, req)
		return w
	}

	do()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("first call: handler executed %d times, want 1", got)
	}

	time.Sleep(40 * time.Millisecond) // past the 20ms ttl
	do()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("after ttl expiry: handler executed %d times total, want 2 (expired cache must not be replayed forever)", got)
	}
}

// TestIdempotencyOversizedResponseIsNotReexecuted covers the oversized-
// success case: the ORIGINAL caller still gets the full real response, but a
// REPLAY must never re-execute the handler's side effect just because the
// response was too large to cache verbatim. Deleting the reservation after
// the side effect already occurred would let a retry execute it again — that
// is the bug this test guards against. The replay instead gets a terminal
// placeholder response at the original status code, never a fresh execution.
func TestIdempotencyOversizedResponseIsNotReexecuted(t *testing.T) {
	old := maxCachedResponseBytes
	maxCachedResponseBytes = 16
	defer func() { maxCachedResponseBytes = old }()

	store := memory.NewIdempotencyRepository()
	var calls int32
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/x", func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		c.JSON(http.StatusAccepted, gin.H{"payload": "this response body is longer than 16 bytes"})
	})

	do := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Idempotency-Key", "oversized-key")
		r.ServeHTTP(w, req)
		return w
	}

	w1 := do()
	if w1.Code != http.StatusAccepted || w1.Body.Len() == 0 {
		t.Fatalf("first call: status=%d body=%q, want the full real response delivered", w1.Code, w1.Body.String())
	}
	w2 := do()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler executed %d times, want 1 (a replay must NEVER re-execute the side effect, even when the original response was too large to cache)", got)
	}
	if w2.Code != http.StatusAccepted {
		t.Errorf("replay status = %d, want 202 (the original status code, from the durable placeholder)", w2.Code)
	}
}

// TestIdempotencyRepositoryFencingTokenBlocksSlowOwner exercises fencing at
// the repository level, directly against
// memory.IdempotencyRepository (not through the HTTP middleware, since the
// scenario requires two DISTINCT Reserve calls racing across an expiry —
// something a single gin round trip can't set up): a caller that Reserves
// key, is slow enough for ttl to elapse, and only THEN tries to Complete
// must not be able to overwrite what a second caller (who Reserved after
// takeover) already completed.
func TestIdempotencyRepositoryFencingTokenBlocksSlowOwner(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	ctx := context.Background()

	// Owner 1 reserves, then is slow — ttl elapses before it ever calls Complete.
	_, reserved1, token1, err := store.Reserve(ctx, "key-1", "POST", "/x", "hash-1", 10*time.Millisecond)
	if err != nil || !reserved1 {
		t.Fatalf("first Reserve: reserved=%v err=%v", reserved1, err)
	}
	time.Sleep(20 * time.Millisecond) // past ttl

	// Owner 2 takes over the now-expired reservation and completes it.
	existing2, reserved2, token2, err := store.Reserve(ctx, "key-1", "POST", "/x", "hash-1", time.Hour)
	if err != nil || !reserved2 {
		t.Fatalf("takeover Reserve: reserved=%v existing=%v err=%v", reserved2, existing2, err)
	}
	if token2 == token1 {
		t.Fatal("takeover must mint a NEW token, not reuse the expired owner's")
	}
	if err := store.Complete(ctx, "key-1", token2, http.StatusAccepted, []byte(`{"owner":2}`)); err != nil {
		t.Fatalf("owner 2 Complete: %v", err)
	}

	// Owner 1, waking up late, tries to Complete with its now-stale token.
	// This MUST be rejected and must NOT clobber owner 2's completed record.
	err = store.Complete(ctx, "key-1", token1, http.StatusAccepted, []byte(`{"owner":1}`))
	if !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Fatalf("stale owner Complete: err = %v, want ErrFencingTokenMismatch", err)
	}

	rec, err := store.Get(ctx, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.ResponseBody) != `{"owner":2}` {
		t.Errorf("stored response = %s, want owner 2's — the stale owner must not have overwritten it", rec.ResponseBody)
	}

	// Owner 1's stale token must not be able to Release owner 2's live
	// reservation out from under it either.
	if err := store.Release(ctx, "key-1", token1); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("stale owner Release: err = %v, want ErrFencingTokenMismatch", err)
	}
	if _, err := store.Get(ctx, "key-1"); err != nil {
		t.Errorf("owner 2's record must still exist after the stale Release attempt: %v", err)
	}
}

// TestIdempotencyRepositoryCompleteRejectsUnknownToken pins the
// completion-failure case that sits alongside fencing:
// Complete/Release against a key that was never Reserved (or
// whose token is simply wrong) must fail explicitly, not silently succeed
// or silently no-op.
func TestIdempotencyRepositoryCompleteRejectsUnknownToken(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	ctx := context.Background()

	if err := store.Complete(ctx, "never-reserved", "bogus-token", http.StatusOK, nil); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("Complete on unreserved key: err = %v, want ErrFencingTokenMismatch", err)
	}
	if err := store.Release(ctx, "never-reserved", "bogus-token"); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("Release on unreserved key: err = %v, want ErrFencingTokenMismatch", err)
	}

	_, reserved, token, err := store.Reserve(ctx, "key-2", "POST", "/x", "hash", time.Hour)
	if err != nil || !reserved {
		t.Fatalf("Reserve: reserved=%v err=%v", reserved, err)
	}
	if err := store.Complete(ctx, "key-2", token+"-wrong", http.StatusOK, nil); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("Complete with wrong token: err = %v, want ErrFencingTokenMismatch", err)
	}
}

func TestRateLimitBlocksAfterBurst(t *testing.T) {
	r := gin.New()
	r.Use(RateLimit(1, 2)) // 2 burst, slow refill
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	var codes []int
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "1.2.3.4:1111"
		r.ServeHTTP(w, req)
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Errorf("expected first 2 requests to pass burst, got %v", codes)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Errorf("expected 3rd request to be rate-limited, got %v", codes)
	}
}
