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

// hashSessionToken returns the SHA-256 digest of a session token. Only the
// digest is stored, as the session's lookup id, so read access to the session
// collection does not yield usable tokens: presenting a stored digest hashes
// to a different value on lookup.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionManager issues and validates short-lived wallet sessions bound to one
// Ethereum address. A session lets an investor act only for its own address and
// is issued after the caller proves wallet ownership through
// compliance.ChallengeService.VerifyOwnership (see the WalletSession security
// scheme in api/openapi.yaml).
//
// Sessions are stored through repository.WalletSessionRepository, which is
// shared by all replicas in the MongoDB backend (with a TTL index) and
// process-local in the in-memory backend used for PERSISTENCE_MODE=memory.
// SessionManager owns token generation (crypto/rand) and the TTL.
type SessionManager struct {
	ttl  time.Duration
	repo repository.WalletSessionRepository
}

// NewSessionManager constructs a SessionManager whose issued tokens expire
// after ttl and are persisted through repo
func NewSessionManager(repo repository.WalletSessionRepository, ttl time.Duration) *SessionManager {
	return &SessionManager{ttl: ttl, repo: repo}
}

// Issue returns a new random opaque session token bound to address, valid for
// the manager's TTL. Every call issues an independent token, so a wallet may
// hold several valid sessions at once, for example in two browser tabs.
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

// Validate resolves token to its bound address. ok is false for an unknown,
// malformed or expired token, and callers must not distinguish those cases in a
// response (see RequireWalletSession). A store error, such as an unreachable
// database, also yields ok == false. Expiry is enforced by repo.Get.
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

// RequireWalletSession resolves the WalletSessionHeader through sm and aborts
// with 401 if it is missing, unrecognized or expired. On success handlers read
// the session's address with WalletSessionAddress. Handlers behind it act only
// for that address, which is what separates it from the admin JWT. A nil sm
// answers 501.
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
