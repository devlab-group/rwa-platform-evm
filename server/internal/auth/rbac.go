// Package auth provides role, idempotency, rate-limit, CORS and security-header
// middleware for the Gin API.
//
// Admin auth is a wallet signature exchanged for a JWT (the BearerJwt scheme in
// api/openapi.yaml). The admin SPA proves control of the configured admin
// wallet through POST /auth/challenge and POST /auth/session and receives an
// HS256 JWT, which it sends as "Authorization: Bearer <jwt>". Authenticate
// verifies the token and sets the request role, and RequireRole gates the admin
// routes. Investor routes use a separate, narrower wallet session
// (SessionManager, X-Wallet-Session header).
package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Role is a coarse server-side permission bucket. A request either carries a
// valid admin JWT (RoleAdmin) or it does not (RoleReadOnly). The finer
// authorities (auditor, compliance, pricer, treasurer, redemption manager) are
// enforced by the contracts' own role checks, so the server only gates which
// HTTP routes a caller may reach.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleReadOnly Role = "readonly"
)

// contextRoleKey is the gin.Context key RequireRole reads from
const contextRoleKey = "auth.role"

// contextPrincipalKey is the gin.Context key PrincipalFromContext reads from.
// It holds the admin wallet address for a valid admin JWT and
// anonymousPrincipal otherwise.
const contextPrincipalKey = "auth.principal"

// anonymousPrincipal is PrincipalFromContext's value for a request without a
// valid admin JWT. All anonymous callers share it, which is harmless for
// idempotency scoping because the routes using Idempotency require RoleAdmin
// first. Authorization stays with RequireRole.
const anonymousPrincipal = "anonymous"

// Authenticate resolves the caller's role from an "Authorization: Bearer <jwt>"
// token verified against jwtSecret. A valid admin JWT sets RoleAdmin and
// records the admin wallet address as the principal. A missing or invalid token
// falls through to RoleReadOnly, so public routes work without one. RequireRole
// enforces the per-route gate.
//
// An empty jwtSecret means no token can verify, so every request is
// RoleReadOnly.
func Authenticate(jwtSecret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := RoleReadOnly
		principal := anonymousPrincipal

		if len(jwtSecret) > 0 {
			if tok := BearerToken(c); tok != "" {
				if addr, err := ParseAdminJWT(jwtSecret, tok); err == nil {
					role = RoleAdmin
					// The wallet address is not secret, so it serves as the principal
					principal = addr
				}
			}
		}

		c.Set(contextRoleKey, role)
		c.Set(contextPrincipalKey, principal)
		c.Next()
	}
}

// PrincipalFromContext returns the identity Authenticate recorded for this
// request: the admin wallet address, or anonymousPrincipal if no valid admin
// JWT was presented
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
// in allowed
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

// RoleFromContext returns the authenticated role, defaulting to RoleReadOnly
func RoleFromContext(c *gin.Context) Role {
	role, ok := c.Get(contextRoleKey)
	if !ok {
		return RoleReadOnly
	}
	r, _ := role.(Role)
	return r
}
