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

// maxIdempotencyKeyBytes bounds caller-supplied header size to limit per-key storage
// growth
const maxIdempotencyKeyBytes = 256

// maxCachedResponseBytes caps replay storage per successful response; responseRecorder
// still sends the full response. Tests may lower this variable.
var maxCachedResponseBytes = 1 << 20 // 1 MiB

// Idempotency requires Idempotency-Key on configured write routes. Within a live
// reservation, store.Reserve admits one handler; matching bodies replay the completed
// response, while changed bodies return 409.
// Keys include the raw header, authenticated principal, HTTP method, route template,
// concrete path and canonical query, isolating callers and path parameters.
// Install after Authenticate and RequireRole so cache hits cannot bypass authorization.
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
		// Include path parameters and query values omitted by the route template
		scopedKey := principal + "\x00" + c.Request.Method + "\x00" + route + "\x00" +
			c.Request.URL.Path + "\x00" + canonicalQuery(c.Request.URL) + "\x00" + rawKey

		var bodyBytes []byte
		if c.Request.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(c.Request.Body)
			if err != nil {
				// Reject failed reads, including body-limit errors, rather than hashing partial
				// input
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
				// A concurrent or crashed request still holds this reservation. Return 409 until
				// completion or TTL expiry; an expired pending reservation can be claimed again.
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
				// The client received the full response, exceeding maxCachedResponseBytes. Complete
				// with a small placeholder at the original status, retaining the reservation so
				// retries cannot repeat the effect while it is live.
				placeholder := []byte(fmt.Sprintf(
					`{"code":"idempotent_response_not_cached","message":"the request completed successfully; its response exceeded the %d-byte idempotency cache limit and was not stored for replay, but the side effect was NOT re-executed"}`,
					maxCachedResponseBytes))
				if err := store.Complete(c.Request.Context(), scopedKey, token, status, placeholder); err != nil {
					logIdempotencyOutboxError("complete_oversized", scopedKey, err)
				}
				return
			}
			if err := store.Complete(c.Request.Context(), scopedKey, token, status, body); err != nil {
				// The caller has its 2xx, but retries receive 409 until TTL expiry. Log the stuck
				// reservation for operators.
				logIdempotencyOutboxError("complete", scopedKey, err)
			}
		} else {
			// Release non-2xx reservations so failed requests can be retried
			if err := store.Release(c.Request.Context(), scopedKey, token); err != nil {
				logIdempotencyOutboxError("release", scopedKey, err)
			}
		}
	}
}

// canonicalQuery sorts keys and their values so parameter order does not change the
// idempotency key, while different values do
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

// logIdempotencyOutboxError reports Complete/Release failures after the response
// through logs and metrics.IdempotencyOutboxErrorsTotal. Logs include the full
// scopedKey and raw Idempotency-Key, which is not a secret.
func logIdempotencyOutboxError(phase, scopedKey string, err error) {
	metrics.IdempotencyOutboxErrorsTotal.WithLabelValues(phase).Inc()
	log.Printf("auth: idempotency store %s failed for key %q: %v (reservation may be stuck until TTL expiry)", phase, scopedKey, err)
}

// responseRecorder forwards response bytes and buffers them for exact replay. Beyond
// maxCachedResponseBytes it discards the buffer and sets truncated.
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
