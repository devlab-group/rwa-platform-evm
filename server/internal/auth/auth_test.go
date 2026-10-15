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

// TestContentSecurityPolicyMatchesWebEngValidatedString pins the policy validated by
// the admin SPA production build; see web/CSP-AUDIT.md
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

// testJWTSecret is an HMAC key of at least 32 bytes for the middleware tests
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

	// No token: forbidden on an admin route
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin-only", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("no token: status = %d, want 403", w.Code)
	}

	// Invalid bearer: forbidden, the request falls through to read-only
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin-only", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("invalid bearer: status = %d, want 403", w.Code)
	}

	// Valid admin JWT: allowed, and the principal is the admin address
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin-only", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("with JWT: status = %d, want 200", w.Code)
	}

	// A public route works without a token
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

// TestIdempotencyConcurrentIdenticalRequestsExecuteHandlerOnce checks atomic
// reservation: one identical request runs the handler; others replay its response or
// receive 409 while it is in progress
func TestIdempotencyConcurrentIdenticalRequestsExecuteHandlerOnce(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls int32
	r := gin.New()
	r.Use(Idempotency(store, time.Hour))
	r.POST("/deploy", func(c *gin.Context) {
		atomic.AddInt32(&calls, 1)
		// Hold the handler open so other goroutines race in, which exposes a
		// Get-then-Save implementation
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
		t.Error("no request observed a 202: every goroutine lost the race, which should not be possible")
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

// TestIdempotencyRequiresHeader checks that a missing Idempotency-Key is
// rejected with a stable code and the handler never runs
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

// TestIdempotencyScopedByRoute checks that two routes receiving the same
// Idempotency-Key and body each execute and do not share a cached response
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

// TestIdempotencyScopedByPathParams checks that identical keys and empty bodies with
// different path parameters execute separately, preventing record B's reissue from
// replaying record A's response
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

// TestIdempotencyScopedByPrincipal requires identical keys, bodies and routes to
// produce separate caches for different principals. Inject the principal directly to
// isolate Idempotency.
func TestIdempotencyScopedByPrincipal(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	var calls int
	r := gin.New()
	// Read the principal from a test header and store it where Authenticate would
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

// TestIdempotencyRequireRoleBeforeMiddlewareBlocksUnauthorizedReplay checks the API's
// middleware order: RequireRole rejects unauthorized callers before Idempotency can
// replay a cached response
func TestIdempotencyRequireRoleBeforeMiddlewareBlocksUnauthorizedReplay(t *testing.T) {
	const adminAddr = "0x2222222222222222222222222222222222222222"
	token, _, err := IssueAdminJWT(testJWTSecret, adminAddr, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	store := memory.NewIdempotencyRepository()
	r := gin.New()
	r.Use(Authenticate(testJWTSecret))
	// Same order as internal/api/api.go: RequireRole, then Idempotency
	r.POST("/x", RequireRole(RoleAdmin), Idempotency(store, time.Hour), func(c *gin.Context) {
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
	})

	body := []byte(`{"a":1}`)
	// The admin request executes and is cached
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("admin call: status = %d, want 202", w.Code)
	}

	// An unauthenticated replay with the same key and body is rejected by
	// RequireRole and never served the admin's cached 202
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req2.Header.Set("Idempotency-Key", "k")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("unauthorized replay: status = %d, want 403 (must never see the cached 2xx)", w2.Code)
	}
}

// TestIdempotencyRejectsOversizedKey checks that an oversized key is rejected
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

// TestIdempotencyExpiredReservationIsTakenOver checks that ExpiresAt is
// enforced. A completed response older than ttl is not replayed, and the next
// identical request runs the handler again.
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

// TestIdempotencyOversizedResponseIsNotReexecuted checks that the original caller gets
// the full response, while replay gets a placeholder at the original status without
// rerunning the handler
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

// TestIdempotencyRepositoryFencingTokenBlocksSlowOwner reserves a memory-store key
// across expiry. A stale owner's Complete must not overwrite the next owner's result.
func TestIdempotencyRepositoryFencingTokenBlocksSlowOwner(t *testing.T) {
	store := memory.NewIdempotencyRepository()
	ctx := context.Background()

	// Owner 1 reserves, then ttl elapses before it calls Complete
	_, reserved1, token1, err := store.Reserve(ctx, "key-1", "POST", "/x", "hash-1", 10*time.Millisecond)
	if err != nil || !reserved1 {
		t.Fatalf("first Reserve: reserved=%v err=%v", reserved1, err)
	}
	time.Sleep(20 * time.Millisecond) // past ttl

	// Owner 2 takes over the expired reservation and completes it
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

	// Owner 1 completes late with a stale token, which must be rejected and
	// must not overwrite owner 2's record
	err = store.Complete(ctx, "key-1", token1, http.StatusAccepted, []byte(`{"owner":1}`))
	if !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Fatalf("stale owner Complete: err = %v, want ErrFencingTokenMismatch", err)
	}

	rec, err := store.Get(ctx, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.ResponseBody) != `{"owner":2}` {
		t.Errorf("stored response = %s, want owner 2's, the stale owner must not have overwritten it", rec.ResponseBody)
	}

	// Owner 1's stale token must not release owner 2's reservation either
	if err := store.Release(ctx, "key-1", token1); !errors.Is(err, repository.ErrFencingTokenMismatch) {
		t.Errorf("stale owner Release: err = %v, want ErrFencingTokenMismatch", err)
	}
	if _, err := store.Get(ctx, "key-1"); err != nil {
		t.Errorf("owner 2's record must still exist after the stale Release attempt: %v", err)
	}
}

// TestIdempotencyRepositoryCompleteRejectsUnknownToken checks that Complete and
// Release fail for a key that was never reserved or a wrong token
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
