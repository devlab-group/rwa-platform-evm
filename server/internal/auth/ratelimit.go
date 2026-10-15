package auth

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// bucketTTL controls retention of idle per-IP buckets, including rotating IPv6
// addresses. It preserves ordinary clients' burst credit between requests but does not
// cap the number of active IPs.
const bucketTTL = 10 * time.Minute

// sweepInterval is the minimum time between eviction passes over all buckets
const sweepInterval = time.Minute

type limiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// RateLimit uses a per-c.ClientIP token bucket with the configured refill rate and
// burst, returning 429 when empty. Buckets are created on first use; request-triggered
// sweeps evict those idle longer than bucketTTL.
// State is process-local. Across replicas, aggregate permitted traffic can reach the
// configured rate times the replica count.
func RateLimit(ratePerSecond float64, burst int) gin.HandlerFunc {
	handler, _ := newRateLimit(ratePerSecond, burst, bucketTTL, sweepInterval, time.Now)
	return handler
}

// newRateLimit is RateLimit with an injectable TTL, sweep interval and clock,
// and a bucket-count accessor, so tests can check eviction without sleeping
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
