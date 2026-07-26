package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/metrics"
)

// maxIdempotencyKeyBytes bounds the caller-supplied Idempotency-Key header.
// An unbounded key is a cheap way to grow the idempotency store without ever
// completing a request.
const maxIdempotencyKeyBytes = 256

// maxCachedResponseBytes bounds how much of a successful response this
// middleware will retain for replay. The real response is always written
// through to the actual caller in full (see responseRecorder); this only
// caps what gets persisted as a REPLAY of a future identical request, so a
// handler with a large success payload can't be turned into an unbounded
// per-key memory/storage sink. A var, not a const, so tests can shrink it
// instead of constructing a real 1 MiB response body.
var maxCachedResponseBytes = 1 << 20 // 1 MiB

// Idempotency implements the Idempotency-Key contract required by every
// state-changing endpoint in api/openapi.yaml: a request replayed with the
// same key and body returns the original response verbatim instead of
// re-executing a side effect; the same key reused with a different body is
// rejected as a conflict; the header itself is REQUIRED. Making it optional
// would let any mutation bypass the protection entirely — every route this
// middleware is wired onto is a side-effecting endpoint (see
// internal/api/api.go's route table), so no legitimate caller benefits from
// skipping it.
//
// Reservation is atomic (store.Reserve), not a separate Get-then-Save: two
// concurrent requests carrying the same key must not both fall through to
// c.Next() and both execute the handler's side effect (e.g. both submit a
// blockchain tx) — see IdempotencyRepository's doc comment for why this is
// the actual point of this middleware, not an optimization.
//
// The caller-supplied key alone is NOT the storage key. Every route that
// wires this middleware MUST run it AFTER auth.Authenticate and any per-route
// auth.RequireRole (see internal/api/api.go's route table) — Idempotency
// namespaces its reservation by:
//   - the authenticated PRINCIPAL (auth.PrincipalFromContext — a digest of
//     the actual X-API-Key, not just the coarser Role bucket: two different
//     API keys that map to the same role must not collide);
//   - the HTTP method and the REGISTERED route template (gin's
//     c.FullPath(), the same normalization internal/metrics.GinMiddleware
//     uses);
//   - the CONCRETE request path and a canonicalized form of the query
//     string. The route template alone (:recordId) is shared by every
//     record, so POST /assets/records/A/reissue and .../B/reissue would
//     otherwise scope to the same key — two different path parameters must
//     never collide on the same Idempotency-Key.
//
// So:
//   - two different routes that happen to receive the same request body can
//     never collide on the same Idempotency-Key (which would otherwise return
//     semantically unrelated success responses);
//   - two different concrete targets of the SAME route (different path
//     params/query) can never collide either;
//   - one principal can never replay a response cached under the same raw
//     key by a DIFFERENT principal, even if that other principal happens to
//     share the same Role;
//   - running RequireRole before this middleware (route table ordering)
//     ensures a replay never returns a cached 2xx body to a caller whose
//     role wouldn't have been allowed to execute the request in the first
//     place — namespacing alone can't provide that if the ordering were
//     reversed, since a cache hit here aborts before RequireRole would run.
func Idempotency(store repository.IdempotencyRepository, ttl time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawKey := c.GetHeader("Idempotency-Key")
		if rawKey == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    "idempotency_key_required",
				"message": "Idempotency-Key header is required for this operation",
			})
			return
		}
		if len(rawKey) > maxIdempotencyKeyBytes {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    "idempotency_key_too_long",
				"message": fmt.Sprintf("Idempotency-Key must be at most %d bytes", maxIdempotencyKeyBytes),
			})
			return
		}

		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path // unmatched route (shouldn't reach a handler that wires this, but never silently mis-scope)
		}
		// principal is deliberately read from context here
		// (PrincipalFromContext), not re-derived from the request, so this
		// middleware's scoping always reflects whatever auth.Authenticate
		// actually decided for THIS request, independent of route
		// registration details.
		principal := PrincipalFromContext(c)
		// The concrete path (c.Request.URL.Path) carries the actual path
		// parameter VALUES (e.g. the real recordId), unlike route, which is
		// only the :recordId TEMPLATE every record shares — this is what
		// closes the A-vs-B cross-record collision.
		// canonicalQuery folds query semantics in too, for routes (if any)
		// that vary behavior by query parameter.
		scopedKey := principal + "\x00" + c.Request.Method + "\x00" + route + "\x00" +
			c.Request.URL.Path + "\x00" + canonicalQuery(c.Request.URL) + "\x00" + rawKey

		var bodyBytes []byte
		if c.Request.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(c.Request.Body)
			if err != nil {
				// Most commonly http.MaxBytesReader (auth.MaxRequestBody,
				// installed earlier in the chain) refusing an oversized
				// body — silently hashing/caching a truncated read here
				// would be worse than surfacing the real error.
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
					"code":    "request_body_too_large",
					"message": "request body could not be read: " + err.Error(),
				})
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		sum := sha256.Sum256(bodyBytes)
		reqHash := hex.EncodeToString(sum[:])

		existing, reserved, token, err := store.Reserve(c.Request.Context(), scopedKey, c.Request.Method, route, reqHash, ttl)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": "internal_error", "message": "idempotency store error"})
			return
		}
		if !reserved {
			if existing.RequestHash != reqHash {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"code":    "idempotency_key_reused",
					"message": "Idempotency-Key was already used with a different request body",
				})
				return
			}
			if existing.ResponseStatus == 0 {
				// Reserved by another request that hasn't Complete()d yet —
				// genuinely concurrent, or a prior attempt crashed before
				// finishing. Either way, executing the handler again here
				// would defeat the reservation; ask the caller to retry.
				// Implementations MUST treat an EXPIRED pending reservation
				// (ExpiresAt in the past) as free for a fresh Reserve to
				// take over instead of returning it here forever — see
				// repository/memory's Reserve — so this state is bounded by
				// ttl, not permanent, even if the original caller crashed.
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"code":    "idempotency_key_in_progress",
					"message": "a request with this Idempotency-Key is still being processed",
				})
				return
			}
			c.Data(existing.ResponseStatus, "application/json", existing.ResponseBody)
			c.Abort()
			return
		}

		rec := &responseRecorder{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = rec
		c.Next()

		status := rec.Status()
		if status >= 200 && status < 300 {
			body := rec.body.Bytes()
			if rec.truncated {
				// The real response (already fully written through to the
				// client above) exceeded maxCachedResponseBytes. The
				// naive behavior would release the reservation here, which
				// would let a RETRY re-execute the handler's side effect a
				// second time — deleting the reservation after the side
				// effect already occurred is exactly what this middleware
				// exists to prevent. Instead, Complete the
				// reservation with a small durable PLACEHOLDER body (never
				// the truncated/corrupt bytes) at the real status code: a
				// future replay gets a terminal 2xx confirming the side
				// effect already happened, and — critically — the handler
				// is never invoked again for this key.
				placeholder := []byte(fmt.Sprintf(
					`{"code":"idempotent_response_not_cached","message":"the request completed successfully; its response exceeded the %d-byte idempotency cache limit and was not stored for replay, but the side effect was NOT re-executed"}`,
					maxCachedResponseBytes))
				if err := store.Complete(c.Request.Context(), scopedKey, token, status, placeholder); err != nil {
					logIdempotencyOutboxError("complete_oversized", scopedKey, err)
				}
				return
			}
			if err := store.Complete(c.Request.Context(), scopedKey, token, status, body); err != nil {
				// The caller already received the real 2xx response above;
				// what's now inconsistent is only the CACHE for a future
				// replay. Left silent, a retry with
				// this key is stuck returning 409 "in progress" until ttl
				// elapses. Surface it so an operator can see the incomplete
				// outbox instead of a client silently failing every retry.
				logIdempotencyOutboxError("complete", scopedKey, err)
			}
		} else {
			// Only successful side effects are idempotency-cached;
			// release the reservation so a client that
			// retries after a genuine failure isn't permanently told
			// "still in progress" for a request that will never complete.
			if err := store.Release(c.Request.Context(), scopedKey, token); err != nil {
				logIdempotencyOutboxError("release", scopedKey, err)
			}
		}
	}
}

// canonicalQuery renders u's query string in a deterministic, semantically
// normalized form (keys sorted, each key's values sorted) so two requests
// that are the same request modulo query-parameter ORDER scope to the same
// idempotency key, while two requests with genuinely different query
// VALUES never do. The reservation key is the full normalized semantic
// target (path parameters plus relevant query values), not just the route
// template.
func canonicalQuery(u *url.URL) string {
	q := u.Query()
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		b.WriteString(url.QueryEscape(k))
		for _, v := range vals {
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}

// logIdempotencyOutboxError records an operationally visible signal for a
// failed Complete/Release call via both the process log and a
// Prometheus counter (internal/metrics.IdempotencyOutboxErrorsTotal), since
// no HTTP response remains in flight at this point to report the failure
// through. scopedKey is logged in full — it embeds the caller's raw
// Idempotency-Key, which is operator-facing debugging data here, not a
// secret.
func logIdempotencyOutboxError(phase, scopedKey string, err error) {
	metrics.IdempotencyOutboxErrorsTotal.WithLabelValues(phase).Inc()
	log.Printf("auth: idempotency store %s failed for key %q: %v (reservation may be stuck until TTL expiry)", phase, scopedKey, err)
}

// responseRecorder tees written response bytes into a bounded buffer while
// still writing through to the real ResponseWriter in full, so a
// successful response can be replayed byte-for-byte on a repeated
// idempotent request — up to maxCachedResponseBytes; see Idempotency's
// truncated handling for what happens beyond that.
type responseRecorder struct {
	gin.ResponseWriter
	body      *bytes.Buffer
	truncated bool
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.truncated {
		if r.body.Len()+len(b) > maxCachedResponseBytes {
			r.truncated = true
			r.body.Reset()
		} else {
			r.body.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) WriteString(s string) (int, error) {
	if !r.truncated {
		if r.body.Len()+len(s) > maxCachedResponseBytes {
			r.truncated = true
			r.body.Reset()
		} else {
			r.body.WriteString(s)
		}
	}
	return r.ResponseWriter.WriteString(s)
}
