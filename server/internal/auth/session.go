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

// hashSessionToken is the one-way lookup key derived from a bearer session
// token: only this SHA-256 digest is ever persisted as the session's storage
// id, never the token itself. Read access to the session collection therefore
// yields only digests — useless as bearer credentials, since presenting a
// digest hashes to a DIFFERENT value on lookup — while a legitimate holder's
// token still resolves by re-deriving the same digest.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionManager issues and validates short-lived wallet sessions bound to
// a single Ethereum address. Investor self-service needs something weaker
// than an admin credential: a session grants read access only to ITS OWN
// address's status, and is minted only after the caller has proved wallet
// ownership via compliance.ChallengeService.VerifyOwnership (see
// api/openapi.yaml's WalletSession securityScheme and the verifyChallenge/
// getMyWalletStatus operations).
//
// Storage is delegated to a shared Mongo store with a TTL index. An earlier
// design held the token map directly in a process-local, mutex-protected map,
// which meant a session minted by one server replica would not validate on
// another replica behind the same load balancer, and a restart lost every
// session. SessionManager still owns token generation (crypto/rand) and the
// TTL policy; repo is repository.WalletSessionRepository — backed by
// repository/mongodb in production (one shared collection across every
// replica, with a TTL index) or repository/memory for PERSISTENCE_MODE=
// memory dev, where the original single-process-only behavior is preserved
// automatically since nothing outside this process can ever reach that map
// anyway.
type SessionManager struct {
	ttl  time.Duration
	repo repository.WalletSessionRepository
}

// NewSessionManager constructs a SessionManager whose issued tokens expire
// after ttl and are persisted through repo.
func NewSessionManager(repo repository.WalletSessionRepository, ttl time.Duration) *SessionManager {
	return &SessionManager{ttl: ttl, repo: repo}
}

// Issue mints a fresh, unguessable opaque session token bound to address,
// expiring after the manager's TTL. Every call issues a NEW independent
// token — unlike an idempotency reservation, nothing here is exclusive or
// single-use; a wallet may hold several concurrently valid sessions (e.g.
// two open browser tabs) without conflict.
func (sm *SessionManager) Issue(ctx context.Context, address string) (token string, expiresAt time.Time, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token = hex.EncodeToString(b)
	expiresAt = time.Now().UTC().Add(sm.ttl)

	// Persist only the token's digest as the lookup id; the raw token is
	// returned to the caller once here and never stored.
	if err := sm.repo.Create(ctx, &models.WalletSession{Token: hashSessionToken(token), Address: address, ExpiresAt: expiresAt}); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// Validate resolves token to its bound address. ok is false for an
// unknown, malformed, or expired token — callers MUST NOT distinguish
// those cases in a response (see RequireWalletSession), so a guess doesn't
// learn anything about which failure mode it hit. A backing-store error
// (e.g. Mongo unreachable) also reports ok==false: there is no other
// reasonable action for a caller here than to treat the session as
// unusable, same as "not found". Expiry is enforced by repo.Get itself
// (repository.WalletSessionRepository's doc comment), not re-checked here.
func (sm *SessionManager) Validate(ctx context.Context, token string) (address string, ok bool) {
	// Look up by the token's digest — the same value stored at Issue — so
	// only a holder of the real token can resolve a session.
	s, err := sm.repo.Get(ctx, hashSessionToken(token))
	if err != nil {
		return "", false
	}
	return s.Address, true
}

// walletSessionContextKey is the gin.Context key WalletSessionAddress reads
// from, set by RequireWalletSession on a successful validation.
const walletSessionContextKey = "auth.walletSessionAddress"

// WalletSessionHeader is the HTTP header carrying the opaque session token
// (api/openapi.yaml's WalletSession securityScheme: "X-Wallet-Session").
const WalletSessionHeader = "X-Wallet-Session"

// RequireWalletSession resolves the WalletSessionHeader via sm and aborts
// with 401 if it is missing, unrecognized, or expired. On success the
// resolved address is available to downstream handlers via
// WalletSessionAddress(c) — there is no way for a handler behind this
// middleware to learn or act on any OTHER address; that is the entire
// point of this being a distinct, narrower auth mechanism from
// RequireRole/X-API-Key.
func RequireWalletSession(sm *SessionManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sm == nil {
			// Matches App's documented nil-dependency contract (internal/api
			// package doc: "handlers that need a nil dependency return a
			// documented 501/400 rather than panicking") — a reduced
			// deployment that never wires a SessionManager disables this
			// route cleanly instead of panicking on first request.
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

// WalletSessionAddress returns the address bound to the request's
// validated wallet session. Only meaningful after RequireWalletSession has
// run on this request (ok is false otherwise — never fabricate an address
// for a request that didn't go through session validation).
func WalletSessionAddress(c *gin.Context) (string, bool) {
	v, ok := c.Get(walletSessionContextKey)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
