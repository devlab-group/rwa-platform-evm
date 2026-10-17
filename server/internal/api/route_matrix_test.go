package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

type routeClass int

const (
	// classAdmin routes need an admin JWT: anonymous callers get 403
	classAdmin routeClass = iota
	// classSession routes need an X-Wallet-Session: callers without one get 401
	classSession
	// classPublic routes carry no role gate; they authenticate in their own way (login
	// signature, webhook HMAC) or serve public data
	classPublic
)

// routeClasses maps NewRouter routes to auth classes; TestRouteAuthorizationMatrix rejects
// missing or stale entries
var routeClasses = map[string]routeClass{
	"GET /healthz": classPublic,
	"GET /readyz":  classPublic,

	"POST /auth/challenge": classPublic,
	"POST /auth/session":   classPublic,

	"GET /api/v1/project":             classPublic,
	"GET /api/v1/project/enforcement": classAdmin,
	"GET /api/v1/config":              classPublic,

	"POST /api/v1/profile/validate": classAdmin,
	"POST /api/v1/profile":          classAdmin,
	"GET /api/v1/profile":           classAdmin,

	"GET /api/v1/compliance/wallets":                classAdmin,
	"POST /api/v1/compliance/challenge":             classPublic,
	"POST /api/v1/compliance/challenge/verify":      classPublic,
	"GET /api/v1/me/wallet-status":                  classSession,
	"POST /api/v1/compliance/kyc/start":             classSession,
	"GET /api/v1/compliance/allowed/:address":       classPublic,
	"GET /api/v1/compliance/webhooks":               classAdmin,
	"POST /api/v1/compliance/status":                classAdmin,
	"POST /api/v1/compliance/webhook":               classPublic, // HMAC-authenticated, not role-gated
	"GET /api/v1/audit-logs":                        classAdmin,
	"GET /api/v1/assets/records":                    classAdmin,
	"POST /api/v1/assets/records":                   classAdmin,
	"GET /api/v1/assets/records/:recordId/package":  classAdmin,
	"POST /api/v1/assets/records/:recordId/reissue": classAdmin,

	"GET /api/v1/assets/burns":                  classAdmin,
	"POST /api/v1/assets/burns":                 classAdmin,
	"GET /api/v1/assets/burns/:burnId/package":  classAdmin,
	"POST /api/v1/assets/burns/:burnId/reissue": classAdmin,

	"GET /api/v1/sales/inventory": classPublic,
	"GET /api/v1/sales/purchases": classAdmin,

	"GET /api/v1/redemptions":     classPublic, // on-chain-public data for investor UIs
	"GET /api/v1/redemptions/:id": classPublic,

	"GET /api/v1/income/distributions":   classAdmin,
	"POST /api/v1/income/distributions":  classAdmin,
	"GET /api/v1/income/claims/:address": classPublic, // snapshots are public on IPFS
	"GET /api/v1/transactions":           classPublic, // on-chain-public data for investor UIs
}

// concretePath fills route parameters with values that pass handler-side format checks
func concretePath(template string) string {
	out := template
	for _, seg := range strings.Split(template, "/") {
		switch seg {
		case ":address":
			out = strings.Replace(out, seg, "0x00000000000000000000000000000000000000a1", 1)
		case "":
		default:
			if strings.HasPrefix(seg, ":") {
				out = strings.Replace(out, seg, "x1", 1)
			}
		}
	}
	return out
}

// TestRouteAuthorizationMatrix checks registered routes: anonymous admin/session calls
// must get 403/401 before handlers run; admin JWTs must clear the admin role gate
func TestRouteAuthorizationMatrix(t *testing.T) {
	env := setupTestApp(t)

	seen := map[string]bool{}
	var keys []string
	for _, ri := range env.router.Routes() {
		key := ri.Method + " " + ri.Path
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := routeClasses[key]; !ok {
			t.Errorf("route %q is registered but not classified in routeClasses; decide whether it is admin, session or public", key)
		}
	}
	for key := range routeClasses {
		if !seen[key] {
			t.Errorf("routeClasses lists %q, which the router no longer registers", key)
		}
	}

	for _, key := range keys {
		class, ok := routeClasses[key]
		if !ok {
			continue
		}
		method, template, _ := strings.Cut(key, " ")
		path := concretePath(template)
		t.Run(key, func(t *testing.T) {
			do := func(headers map[string]string) int {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Content-Type", "application/json")
				for k, v := range headers {
					req.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				env.router.ServeHTTP(w, req)
				return w.Code
			}
			switch class {
			case classAdmin:
				if got := do(nil); got != http.StatusForbidden {
					t.Errorf("anonymous = %d, want 403", got)
				}
				if got := do(map[string]string{"Authorization": env.bearer, "Idempotency-Key": "matrix"}); got == http.StatusForbidden || got == http.StatusUnauthorized {
					t.Errorf("admin JWT = %d, want the handler to run", got)
				}
			case classSession:
				if got := do(nil); got != http.StatusUnauthorized {
					t.Errorf("no X-Wallet-Session = %d, want 401", got)
				}
			case classPublic:
				if got := do(nil); got == http.StatusForbidden {
					t.Errorf("anonymous = 403 on a route classified public")
				}
			}
		})
	}
}
