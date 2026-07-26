// Package metrics exposes the platform's Prometheus /metrics surface:
// HTTP request counters/latency for every route, plus a small
// set of business gauges an operator dashboard/alertmanager can watch
// directly — inventory, pending-redemption count and age, and
// funded-but-unclaimed redemptions (the same conditions
// internal/alerts.EvaluatePendingRedemptionSLA/EvaluateFundedClaimFailure
// check, exposed as gauges too so they can be graphed over time, not just
// alerted on at a threshold).
//
// Deliberately served on its own listener (cmd/platform's METRICS_ADDR),
// not the public API port: metrics are an operational surface for
// Prometheus/Grafana, not something to expose to arbitrary API callers.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_http_requests_total", Help: "HTTP requests by method, route, and status code.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "rwa_http_request_duration_seconds", Help: "HTTP request latency by method and route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	// SalesInventoryTokens is the Vault's current RWA token inventory
	// available for sale, in whole-token units (18-decimal wei amount /
	// 1e18, as a float — precision loss is acceptable for a dashboard
	// gauge; nothing financial reads this metric back).
	SalesInventoryTokens = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_sales_inventory_tokens", Help: "Vault RWA token inventory available for sale (whole tokens).",
	})

	RedemptionsPendingCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_redemptions_pending_count", Help: "Number of redemption requests currently Pending (requested, not yet funded).",
	})
	RedemptionsPendingOldestAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_redemptions_pending_oldest_age_seconds", Help: "Age of the oldest still-Pending redemption request, in seconds (0 if none are Pending).",
	})
	RedemptionsFundedUnclaimedCount = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_redemptions_funded_unclaimed_count", Help: "Number of redemption requests currently Funded but not yet Completed (claimed).",
	})

	AlertsFiredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_alerts_fired_total", Help: "Count of alert-evaluator findings by kind, each time the evaluator ticker runs (not deduplicated across ticks).",
	}, []string{"kind"})

	// StorageUp is 1 when the last periodic Mongo ping succeeded, 0
	// otherwise, so operators can alert on repository degradation (see
	// cmd/platform's storage health-check ticker). Absent entirely when
	// running with PersistenceMode=="memory" (no ticker started).
	StorageUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_storage_up", Help: "1 if the last periodic MongoDB ping succeeded, 0 otherwise.",
	})

	// IdempotencyOutboxErrorsTotal counts IdempotencyRepository.Complete/
	// Release calls that returned an error after the handler's side effect
	// and HTTP response had already happened. A completion failure after
	// the side effect ran leaves an incomplete outbox state, so we make it
	// operationally visible here rather than discarding it. A nonzero rate
	// means some
	// Idempotency-Key reservations are stuck pending (blocking retries with
	// 409 until TTL expiry) even though their side effect already ran once.
	IdempotencyOutboxErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_idempotency_outbox_errors_total", Help: "Idempotency store Complete/Release calls that failed after the handler already ran, by phase.",
	}, []string{"phase"})

	// ReadModelReconcileErrorsTotal counts a read-model reconciler tick
	// (sales/redemption/assets/compliance) that returned an error, by
	// reconciler name. A sustained nonzero rate means that read model is
	// failing to converge, e.g. a database error stalling its
	// generation-swap rebuild. A healthy deployment stays flat at 0.
	ReadModelReconcileErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_read_model_reconcile_errors_total", Help: "Read-model reconciler ticks that returned an error, by reconciler name.",
	}, []string{"reconciler"})
)

// Handler returns the promhttp handler to mount at /metrics on the metrics
// listener.
func Handler() http.Handler {
	return promhttp.Handler()
}

// GinMiddleware records rwa_http_requests_total/rwa_http_request_duration_seconds
// for every request on the main API router. Uses c.FullPath() (the
// registered route template, e.g. "/api/v1/redemptions/:id", not the raw
// URL) as the path label so per-caller values (like a redemption id) don't
// explode the metric's cardinality.
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched" // NoRoute (webui SPA fallback, or a genuine 404) has no registered template
		}
		status := strconv.Itoa(c.Writer.Status())
		httpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}
