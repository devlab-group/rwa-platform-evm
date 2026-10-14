// Package metrics defines the platform's Prometheus metrics: an HTTP request
// counter and latency histogram, and gauges for sales inventory, pending
// redemptions and funded-but-unclaimed redemptions. The redemption gauges
// cover the conditions that internal/alerts.EvaluatePendingRedemptionSLA and
// EvaluateFundedClaimFailure alert on, so they can also be graphed over time.
//
// Handler is served on its own listener (cmd/platform, METRICS_ADDR) and not on
// the public API port, because /metrics is unauthenticated.
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

	// SalesInventoryTokens is the Vault's RWA token inventory available for
	// sale in whole tokens, as a float, so it can lose precision. It is for
	// dashboards only.
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
		Name: "rwa_alerts_fired_total", Help: "Alerts raised, by kind. A condition that persists is counted again on every check.",
	}, []string{"kind"})

	// StorageUp is 1 when the last periodic MongoDB ping succeeded and 0
	// otherwise, so operators can alert on database degradation. The ping
	// ticker in cmd/platform starts only with MongoDB persistence, so the gauge
	// reads 0 until the first ping and stays 0 in memory mode.
	StorageUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_storage_up", Help: "1 if the last periodic MongoDB ping succeeded, 0 otherwise.",
	})

	// ConfigDrift is 1 when the last config-drift check found on-chain wiring
	// that differs from the project record (see project.CheckConfigDrift) and 0
	// when it matched. It is a gauge because drift is a standing condition that
	// stays 1 until the wiring or the record is corrected. It reads 0 until the
	// first check, and a check that fails to read the chain leaves it unchanged.
	ConfigDrift = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_config_drift", Help: "1 if the last config-drift check found on-chain wiring that no longer matches the project record, 0 otherwise.",
	})

	// IdempotencyOutboxErrorsTotal counts IdempotencyRepository.Complete and
	// Release calls that failed after the handler had run and responded. A
	// nonzero rate means some Idempotency-Key reservations are stuck pending, and
	// retries get 409 until the TTL expires although the side effect already
	// ran.
	IdempotencyOutboxErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_idempotency_outbox_errors_total", Help: "Idempotency store Complete/Release calls that failed after the handler already ran, by phase.",
	}, []string{"phase"})

	// ReadModelReconcileErrorsTotal counts reconciler ticks that returned an
	// error, by reconciler name. A sustained nonzero rate means a read model is
	// not converging, for example because a database error stalls its rebuild.
	ReadModelReconcileErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_read_model_reconcile_errors_total", Help: "Read-model reconciler ticks that returned an error, by reconciler name.",
	}, []string{"reconciler"})
)

// Handler returns the promhttp handler for /metrics on the metrics listener
func Handler() http.Handler {
	return promhttp.Handler()
}

// GinMiddleware records rwa_http_requests_total and
// rwa_http_request_duration_seconds for every request on the API router. The
// path label is the registered route template from c.FullPath(), such as
// "/api/v1/redemptions/:id", so per-request values like an id do not multiply
// the label values.
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched" // no registered template, as on the SPA fallback or a 404
		}
		status := strconv.Itoa(c.Writer.Status())
		httpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}
