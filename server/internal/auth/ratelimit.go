package auth

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// bucketTTL is how long a per-IP limiter bucket survives after its last use
// before being evicted. Without eviction the limiter would retain one bucket
// per observed client IP for the process lifetime, so many spoofable or
// rotating IPv6 source addresses could grow the map without bound. The TTL is
// long enough that a legitimately bursty client never loses its accumulated
// burst credit
// between ordinary requests; short enough that a scan rotating through many
// source addresses can't grow the map unboundedly.
const bucketTTL = 10 * time.Minute

// sweepInterval bounds how often a request pays the O(buckets) eviction
// cost; every other request just touches lastUsed and returns immediately.
const sweepInterval = time.Minute

type limiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// RateLimit applies a per-client-IP token bucket (ratePerSecond refill,
// burst capacity), returning 429 once exhausted. Buckets are created
// lazily and evicted after bucketTTL of inactivity.
//
// KNOWN LIMITATION: this limiter's buckets are process-local, same as the
// SessionManager was before its move to the shared store (see session.go's
// doc comment). Behind more than one server replica, a
// credential-verification caller (POST /auth/session in particular — see
// api.go's sessionLoginRatePerSecond/sessionLoginBurst) gets a separate
// bucket per replica it happens to land on, so the EFFECTIVE rate limit
// across the deployment is (configured rate) x (replica count), not the
// configured rate. Unlike sessions, this was deliberately NOT moved to the
// shared Mongo store in the same pass: every check-and-consume here would
// need to be a round trip to stay atomic (a plain read-then-write over a
// network has the same race a rate limiter exists to prevent), which is a
// real latency cost this handler doesn't currently pay, for a
// defense-in-depth control that degrades gracefully (weaker, not absent)
// under multiple replicas. If/when this needs closing, the lowest-risk
// option is reusing repository.NonceLeaseRepository's Mongo collection
// pattern with a short-TTL per-(ip,route) counter document and an atomic
// $inc, not a bespoke second store.
func RateLimit(ratePerSecond float64, burst int) gin.HandlerFunc {
	handler, _ := newRateLimit(ratePerSecond, burst, bucketTTL, sweepInterval, time.Now)
	return handler
}

// newRateLimit is RateLimit's implementation with an injectable TTL/sweep
// interval/clock and a bucket-count accessor, so the eviction behavior can
// be tested deterministically (without sleeping through real
// bucketTTL/sweepInterval windows, and without exporting the bucket map
// itself just for tests).
func newRateLimit(ratePerSecond float64, burst int, ttl, sweep time.Duration, now func() time.Time) (gin.HandlerFunc, func() int) {
	var mu sync.Mutex
	limiters := map[string]*limiterEntry{}
	lastSweep := now()

	handler := func(c *gin.Context) {
		ip := c.ClientIP()
		t := now()

		mu.Lock()
		if t.Sub(lastSweep) > sweep {
			for k, e := range limiters {
				if t.Sub(e.lastUsed) > ttl {
					delete(limiters, k)
				}
			}
			lastSweep = t
		}
		entry, ok := limiters[ip]
		if !ok {
			entry = &limiterEntry{limiter: rate.NewLimiter(rate.Limit(ratePerSecond), burst)}
			limiters[ip] = entry
		}
		entry.lastUsed = t
		lim := entry.limiter
		mu.Unlock()

		if !lim.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    "rate_limited",
				"message": "too many requests",
			})
			return
		}
		c.Next()
	}

	bucketCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(limiters)
	}
	return handler, bucketCount
}
