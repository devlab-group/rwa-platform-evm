package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// hashSessionToken derives the SHA-256 session lookup ID. Storage exposes only the
// digest, not a usable token: submitting that digest hashes it again.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionManager issues and validates short-lived wallet sessions bound to one Ethereum
// address. API callers must prove ownership through
// compliance.ChallengeService.VerifyOwnership before issuance; see api/openapi.yaml's
// WalletSession scheme.
// It owns crypto/rand token generation and TTL configuration.
// repository.WalletSessionRepository shares sessions across server replicas using
// MongoDB with a TTL index, or stores them locally under PERSISTENCE_MODE=memory.
type SessionManager struct {
	ttl  time.Duration
	repo repository.WalletSessionRepository
}

// NewSessionManager constructs a SessionManager whose issued tokens expire
// after ttl and are persisted through repo
func NewSessionManager(repo repository.WalletSessionRepository, ttl time.Duration) *SessionManager {
	return &SessionManager{ttl: ttl, repo: repo}
}

// Issue returns a random opaque token bound to address for the manager's TTL.
// Independent tokens allow concurrent sessions, such as separate browser tabs.
func (sm *SessionManager) Issue(ctx context.Context, address string) (token string, expiresAt time.Time, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token = hex.EncodeToString(b)
	expiresAt = time.Now().UTC().Add(sm.ttl)

	// Store only the digest, the raw token is returned to the caller and never kept
	if err := sm.repo.Create(ctx, &models.WalletSession{Token: hashSessionToken(token), Address: address, ExpiresAt: expiresAt}); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// Validate resolves token to its address. Unknown, malformed or expired tokens and
// store errors all yield ok == false; callers must not distinguish them in responses.
// repo.Get enforces expiry.
func (sm *SessionManager) Validate(ctx context.Context, token string) (address string, ok bool) {
	// Look up by the digest stored at Issue
	s, err := sm.repo.Get(ctx, hashSessionToken(token))
	if err != nil {
		return "", false
	}
	return s.Address, true
}

// walletSessionContextKey is the gin.Context key WalletSessionAddress reads
// from, set by RequireWalletSession on a successful validation
const walletSessionContextKey = "auth.walletSessionAddress"

// WalletSessionHeader is the HTTP header carrying the opaque session token
// (api/openapi.yaml's WalletSession securityScheme: "X-Wallet-Session")
const WalletSessionHeader = "X-Wallet-Session"

// RequireWalletSession resolves WalletSessionHeader through sm, returning 401 for
// missing, unknown or expired tokens, or 501 for nil sm. Handlers use
// WalletSessionAddress and act only for that wallet, independently of admin JWTs.
func RequireWalletSession(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sm == nil {
			// A deployment without a SessionManager disables the route
			c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{
				"code": "not_configured", "message": "wallet sessions are not configured on this server",
			})
			return
		}
		token := c.GetHeader(WalletSessionHeader)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": "unauthorized", "message": WalletSessionHeader + " header is required",
			})
			return
		}
		address, ok := sm.Validate(c.Request.Context(), token)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code": "unauthorized", "message": "wallet session is missing, invalid, or expired",
			})
			return
		}
		c.Set(walletSessionContextKey, address)
		c.Next()
	}
}

// WalletSessionAddress returns the address bound to the request's validated
// wallet session. ok is false unless RequireWalletSession ran on this request.
func WalletSessionAddress(c *gin.Context) (string, bool) {
	v, ok := c.Get(walletSessionContextKey)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
