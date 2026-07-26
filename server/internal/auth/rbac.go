// Package auth provides RBAC, idempotency, and rate-limit middleware for the
// Gin API. Admin auth is wallet-signature -> JWT
// (api/openapi.yaml's BearerJwt scheme, single-admin model): the SPA proves
// control of the configured admin wallet at POST /auth/challenge + /auth/session
// and receives an HMAC-signed JWT it presents as `Authorization: Bearer <jwt>`.
// Authenticate below verifies that token and sets the request role; RequireRole
// gates the actual admin routes. Investor read routes use a separate,
// narrower wallet SessionManager (X-Wallet-Session), unchanged.
package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Role is a coarse, server-side permission bucket. The platform is
// single-admin: a request either carries a valid admin JWT (RoleAdmin) or it
// does not (RoleReadOnly). The finer authorities (auditor/compliance/pricer/
// treasurer/redemption-manager) live on-chain and are independently
// re-authorized by each contract's role checks — so server RBAC only needs to
// gate which HTTP routes a caller may reach.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleReadOnly Role = "readonly"
)

// contextRoleKey is the gin.Context key RequireRole reads from.
const contextRoleKey = "auth.role"

// contextPrincipalKey is the gin.Context key PrincipalFromContext reads from.
// Idempotency scoping needs a stable per-credential identity, not just the
// coarse Role bucket. For an admin JWT this is the
// admin wallet address; for an unauthenticated caller it is anonymousPrincipal.
const contextPrincipalKey = "auth.principal"

// anonymousPrincipal is PrincipalFromContext's value for a request that
// presented no valid admin JWT. It is a single shared bucket (every anonymous
// caller collides on it), which is fine for idempotency scoping specifically
// because RequireRole runs before Idempotency on every state-changing route
// (see api.go) and those routes require RoleAdmin, which an anonymous caller
// (RoleReadOnly) can never satisfy; PrincipalFromContext never gates
// authorization itself, RequireRole still does that.
const anonymousPrincipal = "anonymous"

// Authenticate resolves the caller's role from an `Authorization: Bearer <jwt>`
// admin token verified against jwtSecret. A valid admin JWT sets RoleAdmin and
// records the admin wallet address as the request principal; a missing or
// invalid token falls through to RoleReadOnly (so public read routes keep
// working without a token). RequireRole enforces the actual gate per route.
//
// jwtSecret being empty (admin JWT auth not configured) means no token can ever
// verify, so every request is RoleReadOnly — matching every other
// optional-dependency degrade in this codebase.
func Authenticate(jwtSecret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := RoleReadOnly
		principal := anonymousPrincipal

		if len(jwtSecret) > 0 {
			if tok := BearerToken(c); tok != "" {
				if addr, err := ParseAdminJWT(jwtSecret, tok); err == nil {
					role = RoleAdmin
					// The admin wallet address is a stable, non-secret identity —
					// use it directly as the idempotency/audit principal.
					principal = addr
				}
			}
		}

		c.Set(contextRoleKey, role)
		c.Set(contextPrincipalKey, principal)
		c.Next()
	}
}

// PrincipalFromContext returns the stable per-credential identity Authenticate
// recorded for this request (the admin wallet address, or anonymousPrincipal
// if no valid admin JWT was presented).
func PrincipalFromContext(c *gin.Context) string {
	v, ok := c.Get(contextPrincipalKey)
	if !ok {
		return anonymousPrincipal
	}
	s, _ := v.(string)
	if s == "" {
		return anonymousPrincipal
	}
	return s
}

// RequireRole rejects the request with 403 unless the authenticated role is
// in allowed.
func RequireRole(allowed ...Role) gin.HandlerFunc {
	set := make(map[Role]bool, len(allowed))
	for _, r := range allowed {
		set[r] = true
	}
	return func(c *gin.Context) {
		role, _ := c.Get(contextRoleKey)
		r, _ := role.(Role)
		if !set[r] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    "forbidden",
				"message": "caller role is not authorized for this operation",
			})
			return
		}
		c.Next()
	}
}

// RoleFromContext returns the authenticated role, defaulting to RoleReadOnly.
func RoleFromContext(c *gin.Context) Role {
	role, ok := c.Get(contextRoleKey)
	if !ok {
		return RoleReadOnly
	}
	r, _ := role.(Role)
	return r
}
