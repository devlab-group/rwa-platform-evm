package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func doRequest(handler gin.HandlerFunc, remoteAddr string) int {
	r := gin.New()
	r.Use(handler)
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestRateLimitAllowsBurstThenBlocks(t *testing.T) {
	handler := RateLimit(1, 2) // 1/s refill, burst 2
	addr := "10.0.0.1:1234"

	if code := doRequest(handler, addr); code != http.StatusOK {
		t.Fatalf("request 1 = %d, want 200", code)
	}
	if code := doRequest(handler, addr); code != http.StatusOK {
		t.Fatalf("request 2 = %d, want 200", code)
	}
	if code := doRequest(handler, addr); code != http.StatusTooManyRequests {
		t.Fatalf("request 3 = %d, want 429", code)
	}
}

func TestRateLimitPerIPIsolation(t *testing.T) {
	handler := RateLimit(1, 1)
	if code := doRequest(handler, "10.0.0.1:1"); code != http.StatusOK {
		t.Fatalf("ip1 request = %d, want 200", code)
	}
	if code := doRequest(handler, "10.0.0.2:1"); code != http.StatusOK {
		t.Fatalf("ip2 request = %d, want 200 (separate bucket)", code)
	}
}

// TestRateLimitEvictsStaleBuckets uses a fake clock to check that a later sweep removes
// buckets idle beyond TTL, without real sleeps
func TestRateLimitEvictsStaleBuckets(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	handler, bucketCount := newRateLimit(100, 100, 5*time.Second, time.Second, clock)

	for i := 0; i < 50; i++ {
		doRequest(handler, "10.0.1.1:1")
	}
	if got := bucketCount(); got != 1 {
		t.Fatalf("bucketCount after 50 requests from 1 IP = %d, want 1", got)
	}

	// A different IP arrives well past ttl+sweep, and its request's sweep must
	// evict the first IP's stale bucket
	now = now.Add(10 * time.Second)
	doRequest(handler, "10.0.2.2:1")

	if got := bucketCount(); got != 1 {
		t.Fatalf("bucketCount after eviction = %d, want 1 (only the fresh IP's bucket)", got)
	}
}

// TestRateLimitDoesNotEvictActiveBuckets checks that a bucket used within ttl
// survives any number of sweeps
func TestRateLimitDoesNotEvictActiveBuckets(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	handler, bucketCount := newRateLimit(1000, 1000, 5*time.Second, time.Second, clock)

	for i := 0; i < 20; i++ {
		now = now.Add(2 * time.Second) // less than ttl between each touch
		doRequest(handler, "10.0.3.3:1")
	}
	if got := bucketCount(); got != 1 {
		t.Fatalf("bucketCount for a continuously active IP = %d, want 1 (never evicted)", got)
	}
}
