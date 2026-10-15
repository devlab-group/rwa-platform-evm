package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/gin-gonic/gin"
)

// BearerToken returns the token from an "Authorization: Bearer <token>"
// header, or "" if the header is absent or not a bearer credential
func BearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// PrincipalDigest returns the hex SHA-256 digest of s, a stable identity label
// that does not reveal s
func PrincipalDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
