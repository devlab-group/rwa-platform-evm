package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/dal/memory"
)

// TestGetProfileReturnsStoredProfile: after a profile is stored (setupTestApp
// seeds one), GET /api/v1/profile returns it — the raw document plus the
// server-derived identity fields — so the admin UI can repopulate after reload.
func TestGetProfileReturnsStoredProfile(t *testing.T) {
	env := setupTestApp(t)

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/profile", nil,
		map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var resp dto.StoredProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ProjectID != "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61" {
		t.Errorf("projectId = %q", resp.ProjectID)
	}
	if resp.ProfileDigest == "" {
		t.Error("expected a non-empty profileDigest")
	}
	if resp.Decimals != 18 || resp.TokenUnit != "gram" {
		t.Errorf("decimals=%d tokenUnit=%q, want 18/gram", resp.Decimals, resp.TokenUnit)
	}
	// profile is the raw stored document — it must round-trip to an object
	// carrying the original fields.
	var doc map[string]any
	if err := json.Unmarshal(resp.Profile, &doc); err != nil {
		t.Fatalf("profile is not a JSON object: %v", err)
	}
	if doc["assetType"] != "allocated-gold-bar" {
		t.Errorf("profile.assetType = %v, want allocated-gold-bar", doc["assetType"])
	}
}

// TestGetProfileAdminOnly: the endpoint is gated like createProfile — a caller
// with no admin JWT is rejected, the admin is allowed.
func TestGetProfileAdminOnly(t *testing.T) {
	env := setupTestApp(t)

	no := doJSON(t, env.router, http.MethodGet, "/api/v1/profile", nil, nil)
	if no.Code != http.StatusForbidden {
		t.Fatalf("no-auth status = %d, want 403", no.Code)
	}
	ok := doJSON(t, env.router, http.MethodGet, "/api/v1/profile", nil,
		map[string]string{"Authorization": env.bearer})
	if ok.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", ok.Code)
	}
}

// TestGetProfileNotFound: with no profile stored, the admin gets a clean 404.
func TestGetProfileNotFound(t *testing.T) {
	repos := memory.New()
	adminKey, _ := crypto.GenerateKey()
	adminAddr := crypto.PubkeyToAddress(adminKey.PublicKey)
	jwtSecret := []byte("test-jwt-secret-that-is-at-least-32-bytes-long")
	app := &App{
		Repos: repos, ChainID: 31337, Audit: auditlog.New(repos.AuditLogs),
		AdminAddress: adminAddr.Hex(), JWTSecret: jwtSecret, JWTTTL: time.Hour,
	}
	router := NewRouter(app)
	token, _, err := auth.IssueAdminJWT(jwtSecret, adminAddr.Hex(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	w := doJSON(t, router, http.MethodGet, "/api/v1/profile", nil,
		map[string]string{"Authorization": "Bearer " + token})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}
