package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouteAuthorizationMatrix asserts an authorization outcome for every route
// NewRouter registers. Each route is called without an Authorization header (a
// missing or invalid admin JWT reads as RoleReadOnly; see auth.Authenticate). A
// gated route must return 403 before the handler runs, whatever the body or path
// parameters, because RequireRole is upstream middleware. A public route must not
// return 403, though it may fail for other reasons (bad params, 404, 501
// not_configured) that are not authorization decisions.
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
		{http.MethodGet, "/api/v1/project/enforcement", true},
		{http.MethodGet, "/api/v1/config", false},

		{http.MethodPost, "/api/v1/profile/validate", true},
		{http.MethodPost, "/api/v1/profile", true},

		{http.MethodGet, "/api/v1/compliance/wallets", true},
		{http.MethodPost, "/api/v1/compliance/challenge", false},
		{http.MethodPost, "/api/v1/compliance/challenge/verify", false},
		{http.MethodGet, "/api/v1/compliance/webhooks", true},
		{http.MethodPost, "/api/v1/compliance/status", true},
		{http.MethodPost, "/api/v1/compliance/webhook", false}, // HMAC-authenticated, not role-gated

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
				t.Errorf("gated route with no admin JWT = %d, want 403 (body=%s)", w.Code, w.Body.String())
			}
			if !rt.gated && w.Code == http.StatusForbidden {
				t.Errorf("public route with no admin JWT = 403, want anything else (this route should not require a role)")
			}
		})
	}
}
