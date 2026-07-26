package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/dal/memory"
)

func TestSessionManagerIssueThenValidate(t *testing.T) {
	ctx := context.Background()
	sm := NewSessionManager(memory.NewWalletSessionRepository(), time.Hour)
	token, expiresAt, err := sm.Issue(ctx, "0xAAAA000000000000000000000000000000AAAA")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if !expiresAt.After(time.Now().UTC()) {
		t.Errorf("expiresAt = %v, want a time in the future", expiresAt)
	}

	address, ok := sm.Validate(ctx, token)
	if !ok {
		t.Fatal("expected a freshly issued token to validate")
	}
	if address != "0xAAAA000000000000000000000000000000AAAA" {
		t.Errorf("address = %q", address)
	}
}

func TestSessionManagerValidateRejectsUnknownToken(t *testing.T) {
	ctx := context.Background()
	sm := NewSessionManager(memory.NewWalletSessionRepository(), time.Hour)
	if _, ok := sm.Validate(ctx, "never-issued"); ok {
		t.Fatal("expected an unknown token to be rejected")
	}
}

// TestSessionManagerValidateRejectsExpiredToken pins the TTL enforcement: a
// WalletSession is a ~15 minute bearer, not a forever-valid one.
func TestSessionManagerValidateRejectsExpiredToken(t *testing.T) {
	ctx := context.Background()
	sm := NewSessionManager(memory.NewWalletSessionRepository(), 10*time.Millisecond)
	token, _, err := sm.Issue(ctx, "0xBBBB000000000000000000000000000000BBBB")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, ok := sm.Validate(ctx, token); ok {
		t.Fatal("expected an expired token to be rejected")
	}
}

// TestSessionManagerIssueIsIndependentPerCall pins that Issue is not
// exclusive/single-use the way idempotency reservation is — two sessions
// for the same address coexist (e.g. two open browser tabs).
func TestSessionManagerIssueIsIndependentPerCall(t *testing.T) {
	ctx := context.Background()
	sm := NewSessionManager(memory.NewWalletSessionRepository(), time.Hour)
	t1, _, _ := sm.Issue(ctx, "0xCCCC000000000000000000000000000000CCCC")
	t2, _, _ := sm.Issue(ctx, "0xCCCC000000000000000000000000000000CCCC")
	if t1 == t2 {
		t.Fatal("expected two independent tokens, got the same value")
	}
	if _, ok := sm.Validate(ctx, t1); !ok {
		t.Error("expected first token to still validate")
	}
	if _, ok := sm.Validate(ctx, t2); !ok {
		t.Error("expected second token to still validate")
	}
}

// TestSessionManagerCrossReplicaValidateAndLogout covers the shared-store
// behavior: two SessionManager instances that each simulate a
// separate server replica but share the SAME backing repository (exactly
// what repository/mongodb gives every real replica in production) must see
// each other's sessions — a token minted by "replica A" validates on
// "replica B", and deleting it through the shared store makes it stop
// validating on "replica A" too. With a process-local map, each
// SessionManager held its own tokens, so this was impossible: a
// second replica could never see a first replica's tokens at all.
func TestSessionManagerCrossReplicaValidateAndLogout(t *testing.T) {
	ctx := context.Background()
	sharedStore := memory.NewWalletSessionRepository()
	replicaA := NewSessionManager(sharedStore, time.Hour)
	replicaB := NewSessionManager(sharedStore, time.Hour)

	token, _, err := replicaA.Issue(ctx, "0xEEEE000000000000000000000000000000EEEE")
	if err != nil {
		t.Fatal(err)
	}

	address, ok := replicaB.Validate(ctx, token)
	if !ok {
		t.Fatal("expected a token issued by replica A to validate on replica B")
	}
	if address != "0xEEEE000000000000000000000000000000EEEE" {
		t.Errorf("address = %q", address)
	}

	// SessionManager itself has no Revoke (wallet sessions expire on TTL
	// only) — exercise the shared store's Delete directly to prove
	// replica-independence of revocation too. The stored id is the token's
	// digest, so revocation through the store keys off the same digest, not
	// the raw token.
	if err := sharedStore.Delete(ctx, hashSessionToken(token)); err != nil {
		t.Fatal(err)
	}
	if _, ok := replicaA.Validate(ctx, token); ok {
		t.Fatal("expected the token deleted via the shared store to stop validating on replica A too")
	}
}

func TestRequireWalletSessionRejectsMissingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sm := NewSessionManager(memory.NewWalletSessionRepository(), time.Hour)
	r := gin.New()
	r.GET("/x", RequireWalletSession(sm), func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireWalletSessionRejectsInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sm := NewSessionManager(memory.NewWalletSessionRepository(), time.Hour)
	r := gin.New()
	r.GET("/x", RequireWalletSession(sm), func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(WalletSessionHeader, "garbage")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestRequireWalletSessionAcceptsValidTokenAndSetsAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sm := NewSessionManager(memory.NewWalletSessionRepository(), time.Hour)
	token, _, err := sm.Issue(context.Background(), "0xDDDD000000000000000000000000000000DDDD")
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	var gotAddress string
	var gotOK bool
	r.GET("/x", RequireWalletSession(sm), func(c *gin.Context) {
		gotAddress, gotOK = WalletSessionAddress(c)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(WalletSessionHeader, token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !gotOK || gotAddress != "0xDDDD000000000000000000000000000000DDDD" {
		t.Errorf("WalletSessionAddress = (%q, %v)", gotAddress, gotOK)
	}
}

// TestRequireWalletSessionNilManagerReturns501 pins App's documented
// nil-dependency contract: a reduced deployment without a SessionManager
// wired disables this route cleanly (501) instead of panicking.
func TestRequireWalletSessionNilManagerReturns501(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", RequireWalletSession(nil), func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", w.Code)
	}
}

// TestWalletSessionAddressWithoutMiddlewareReturnsFalse pins that a
// handler can never fabricate/inherit an address without
// RequireWalletSession having actually run and validated a token.
func TestWalletSessionAddressWithoutMiddlewareReturnsFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var gotOK bool
	r.GET("/x", func(c *gin.Context) {
		_, gotOK = WalletSessionAddress(c)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)
	if gotOK {
		t.Error("expected WalletSessionAddress to report false with no RequireWalletSession in the chain")
	}
}
