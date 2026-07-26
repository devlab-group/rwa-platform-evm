package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouteAuthorizationMatrix asserts an authorization outcome for every
// route. It exercises every route
// NewRouter registers with NO X-API-Key header (a missing key reads as
// RoleReadOnly per auth.Authenticate — see api.go's operatorOrAdmin doc
// comment) and asserts the expected outcome: a gated route must reject
// with 403 before the handler ever runs (regardless of a missing/invalid
// body or path parameter — RequireRole is upstream middleware); a public
// route must NOT be rejected with 403 (it may still fail for other
// reasons — bad params, 404, 501 not_configured — none of which is an
// authorization decision).
func TestRouteAuthorizationMatrix(t *testing.T) {
	env := setupTestApp(t)

	type route struct {
		method string
		path   string
		gated  bool
	}
	routes := []route{
		{http.MethodGet, "/healthz", false},
		{http.MethodGet, "/readyz", false},

		{http.MethodGet, "/api/v1/project", false},
		{http.MethodGet, "/api/v1/config", false},

		{http.MethodPost, "/api/v1/profile/validate", false},
		{http.MethodPost, "/api/v1/profile", true},

		{http.MethodGet, "/api/v1/compliance/wallets", true},
		{http.MethodPost, "/api/v1/compliance/challenge", false},
		{http.MethodPost, "/api/v1/compliance/challenge/verify", false},
		{http.MethodGet, "/api/v1/compliance/webhooks", true},
		{http.MethodPost, "/api/v1/compliance/status", true},
		{http.MethodPost, "/api/v1/compliance/webhook", false}, // HMAC-authenticated, not X-API-Key

		{http.MethodGet, "/api/v1/audit-logs", true},

		{http.MethodGet, "/api/v1/assets/records", true},
		{http.MethodPost, "/api/v1/assets/records", true},
		{http.MethodGet, "/api/v1/assets/records/rec-1/package", true},

		{http.MethodGet, "/api/v1/sales/inventory", false},
		{http.MethodGet, "/api/v1/sales/purchases", true},

		{http.MethodGet, "/api/v1/redemptions", false}, // public: on-chain-public data for investor UIs
		{http.MethodGet, "/api/v1/redemptions/1", false},

		{http.MethodGet, "/api/v1/transactions", false}, // public: on-chain-public data for investor UIs
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			env.router.ServeHTTP(w, req)

			if rt.gated && w.Code != http.StatusForbidden {
				t.Errorf("gated route with no API key = %d, want 403 (body=%s)", w.Code, w.Body.String())
			}
			if !rt.gated && w.Code == http.StatusForbidden {
				t.Errorf("public route with no API key = 403, want anything else (this route should not require a role)")
			}
		})
	}
}
