// Package auth provides Gin middleware for roles, idempotency, rate limits, CORS and
// security headers.
// The admin SPA exchanges wallet proof through POST /auth/challenge and POST
// /auth/session for an HS256 JWT, sent as Authorization: Bearer <jwt>
// (api/openapi.yaml's BearerJwt). Authenticate verifies it; RequireRole gates admin
// routes. Investors use separate address-bound sessions through SessionManager and
// X-Wallet-Session.
package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Role gates HTTP access: valid admin JWTs get RoleAdmin; other requests get
// RoleReadOnly. Contracts separately enforce auditor, compliance, pricer, treasurer and
// redemption-manager authorities.
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

// anonymousPrincipal groups requests without valid admin JWTs. Idempotency routes
// require RoleAdmin first, preventing anonymous cache access; RequireRole owns
// authorization.
const anonymousPrincipal = "anonymous"

// Authenticate validates bearer JWTs with jwtSecret and matches subjects to adminAddress
// case-insensitively. Accepted tokens set RoleAdmin and the wallet principal; other requests stay
// RoleReadOnly/anonymous. Empty jwtSecret or adminAddress disables admin authentication. Rotating
// to another admin address rejects the previous admin's tokens; RequireRole gates protected routes.
func Authenticate(jwtSecret []byte, adminAddress string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := RoleReadOnly
		principal := anonymousPrincipal

		if len(jwtSecret) > 0 && adminAddress != "" {
			if tok := BearerToken(c); tok != "" {
				if addr, err := ParseAdminJWT(jwtSecret, tok); err == nil && strings.EqualFold(addr, adminAddress) {
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
