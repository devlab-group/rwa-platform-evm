package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/gin-gonic/gin"
)

// BearerToken extracts the token from an "Authorization: Bearer <token>"
// header, or "" if the header is absent or not a bearer credential. Shared by
// the admin JWT middleware (Authenticate) and any other bearer consumer.
func BearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// PrincipalDigest is the stable, non-reversible per-credential identity used
// for idempotency scoping and audit-actor attribution — a SHA-256 digest of
// the input, never the input itself. Used by opsctl to derive an audit actor
// label from a chosen actor string without persisting it verbatim.
func PrincipalDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
