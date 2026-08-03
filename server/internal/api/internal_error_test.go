package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// leakyErr stands in for what the Mongo driver, the chain RPC client and the
// IPFS client actually put in an error: hosts, ports, endpoint URLs, collection
// names. None of it may reach an HTTP response.
var leakyErr = errors.New(
	`server selection error: connection() error occurred during connection handshake: ` +
		`dial tcp 10.4.2.17:27017: connect: connection refused, rs: rwa-rs0, db: rwa_platform, collection: transactions`)

// failingTxRepo fails only ListPage; embedding the interface supplies the rest
// (nil, so anything else would panic loudly rather than silently pass).
type failingTxRepo struct {
	repository.TransactionRepository
	err error
}

func (r *failingTxRepo) ListPage(ctx context.Context, address, cursor string, limit int) ([]*models.Transaction, string, error) {
	return nil, "", r.err
}

// A 500 on a PUBLIC route must not describe the failure. GET /api/v1/transactions
// is unauthenticated by design, so anything failInternal echoes is readable by
// anyone who polls it while a backend is unhealthy.
func TestInternalErrorOnPublicRouteLeaksNothing(t *testing.T) {
	env := setupTestApp(t)
	env.app.Repos.Transactions = &failingTxRepo{err: leakyErr}

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/transactions", nil, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	for _, secret := range []string{
		"10.4.2.17", "27017", "rwa-rs0", "rwa_platform", "collection", "connection refused", "handshake",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("response leaks %q: %s", secret, body)
		}
	}

	// The stable machine-readable code is part of the contract and must survive.
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != CodeInternal {
		t.Fatalf("code = %q, want %q", resp.Code, CodeInternal)
	}
	// An opaque message is useless for support unless the operator can tie it
	// back to the logged error, which is what the reference is for.
	if !strings.Contains(resp.Message, "reference ") {
		t.Fatalf("message carries no log reference: %q", resp.Message)
	}
}

// Two 500s must not reuse a reference, or it cannot identify a log line.
func TestInternalErrorReferenceIsPerRequest(t *testing.T) {
	env := setupTestApp(t)
	env.app.Repos.Transactions = &failingTxRepo{err: leakyErr}

	first := doJSON(t, env.router, http.MethodGet, "/api/v1/transactions", nil, nil).Body.String()
	second := doJSON(t, env.router, http.MethodGet, "/api/v1/transactions", nil, nil).Body.String()
	if first == second {
		t.Fatalf("both responses carry the same reference: %s", first)
	}
}

// failInternal must not swallow an oversized-body failure into an opaque 500 —
// that one IS the caller's fault and stays a 413 they can act on.
func TestInternalErrorKeepsOversizedBodyAs413(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/profile", nil)

	failInternal(c, &http.MaxBytesError{Limit: 1024})

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
}
