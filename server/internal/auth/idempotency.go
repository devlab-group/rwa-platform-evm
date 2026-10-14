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

// maxCachedResponseBytes bounds how much of a successful response is stored
// for replay. The caller always receives the full response (see
// responseRecorder), so a large payload cannot become an unbounded per-key
// storage sink. It is a var so tests can shrink it.
var maxCachedResponseBytes = 1 << 20 // 1 MiB

// Idempotency enforces the required Idempotency-Key header on wired write routes.
// The same key and body replay the original response; a different body returns 409.
// store.Reserve claims the key atomically so concurrent requests cannot both
// enter the handler under a live reservation; see IdempotencyRepository.
//
// The stored key combines the raw header with the authenticated principal,
// HTTP method, registered route template, concrete path and canonical query.
// Principal scoping isolates cached responses; concrete paths keep requests
// such as /assets/records/A/reissue and /assets/records/B/reissue distinct.
//
// Install after Authenticate and RequireRole: cache hits abort the chain,
// so authorization must run before a cached response can be served.
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
			route = c.Request.URL.Path // not expected on a wired route, but never scope by an empty route
		}
		// The principal comes from Authenticate's decision for this request
		principal := PrincipalFromContext(c)
		// The concrete path carries the path parameter values that the route
		// template does not, and canonicalQuery covers routes that vary by
		// query parameter
		scopedKey := principal + "\x00" + c.Request.Method + "\x00" + route + "\x00" +
			c.Request.URL.Path + "\x00" + canonicalQuery(c.Request.URL) + "\x00" + rawKey

		var bodyBytes []byte
		if c.Request.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(c.Request.Body)
			if err != nil {
				// Usually MaxRequestBody refusing an oversized body. Hashing a
				// truncated read would be worse than reporting the error.
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
				// Another request holds the reservation and has not completed,
				// either because it is concurrent or because it crashed. Running
				// the handler again would defeat the reservation, so the caller
				// retries. Reserve treats an expired pending reservation as free,
				// so this state lasts at most ttl.
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
				// The client already received the full response, which exceeded
				// maxCachedResponseBytes. Releasing the reservation would let a
				// retry repeat the side effect, so complete it with a small
				// placeholder body at the real status code. A replay then gets a
				// 2xx confirming the side effect happened, and the handler never
				// runs again for this key.
				placeholder := []byte(fmt.Sprintf(
					`{"code":"idempotent_response_not_cached","message":"the request completed successfully; its response exceeded the %d-byte idempotency cache limit and was not stored for replay, but the side effect was NOT re-executed"}`,
					maxCachedResponseBytes))
				if err := store.Complete(c.Request.Context(), scopedKey, token, status, placeholder); err != nil {
					logIdempotencyOutboxError("complete_oversized", scopedKey, err)
				}
				return
			}
			if err := store.Complete(c.Request.Context(), scopedKey, token, status, body); err != nil {
				// The caller already has its 2xx response, but a retry with this
				// key returns 409 "in progress" until ttl elapses. Log it so an
				// operator can see the stuck reservation.
				logIdempotencyOutboxError("complete", scopedKey, err)
			}
		} else {
			// Only 2xx responses are cached. Release the reservation so a retry
			// after a failure is not told the request is still in progress.
			if err := store.Release(c.Request.Context(), scopedKey, token); err != nil {
				logIdempotencyOutboxError("release", scopedKey, err)
			}
		}
	}
}

// canonicalQuery renders u's query string with keys sorted and each key's
// values sorted, so requests that differ only in parameter order share an
// idempotency key and requests with different values do not
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

// logIdempotencyOutboxError reports a failed Complete or Release through the
// process log and metrics.IdempotencyOutboxErrorsTotal, because the response
// has already been sent. scopedKey is logged in full, including the caller's
// raw Idempotency-Key, which is not a secret.
func logIdempotencyOutboxError(phase, scopedKey string, err error) {
	metrics.IdempotencyOutboxErrorsTotal.WithLabelValues(phase).Inc()
	log.Printf("auth: idempotency store %s failed for key %q: %v (reservation may be stuck until TTL expiry)", phase, scopedKey, err)
}

// responseRecorder copies written response bytes into a buffer while writing
// them through to the real ResponseWriter, so a successful response can be
// replayed byte for byte. Beyond maxCachedResponseBytes it stops buffering and
// sets truncated.
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
