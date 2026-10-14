package auth

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// bucketTTL is how long a per-IP bucket is kept after its last use. Eviction
// keeps clients rotating through many source addresses, such as IPv6, from
// growing the map without bound. The TTL is long enough that an ordinary
// client does not lose its burst credit between requests.
const bucketTTL = 10 * time.Minute

// sweepInterval is the minimum time between eviction passes over all buckets
const sweepInterval = time.Minute

type limiterEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// RateLimit applies a token bucket per client IP (c.ClientIP) with the given
// refill rate and burst, and answers 429 once a bucket is empty. Buckets are
// created on first use and evicted after bucketTTL without traffic.
//
// Buckets live in process memory. With several server replicas each replica
// keeps its own bucket, so the effective limit across the deployment is the
// configured rate times the replica count. Sharing the state would cost a
// database round trip on every request to stay atomic.
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
