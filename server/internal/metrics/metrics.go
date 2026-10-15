// Package metrics defines Prometheus HTTP, inventory, redemption, storage, drift and
// error metrics. Redemption gauges track conditions checked by
// internal/alerts.EvaluatePendingRedemptionSLA and EvaluateFundedClaimFailure.
//
// cmd/platform serves the unauthenticated /metrics handler on a separate METRICS_ADDR
// listener.
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

	// SalesInventoryTokens is the Vault's sale inventory in whole tokens; its float
	// representation can lose precision and is for dashboards only
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

	AllowedWallets = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_allowed_wallets", Help: "Unexpired Allowed wallets in the investor read model, excluding Vault and redemption escrow.",
	})

	AlertsFiredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_alerts_fired_total", Help: "Alerts raised, by kind. A condition that persists is counted again on every check.",
	}, []string{"kind"})

	// StorageUp is 1 if the last periodic MongoDB ping succeeded, otherwise 0. It starts
	// at 0 and stays there in memory mode, where cmd/platform runs no ping ticker.
	StorageUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_storage_up", Help: "1 if the last periodic MongoDB ping succeeded, 0 otherwise.",
	})

	// ConfigDrift is 1 when project.CheckConfigDrift last found wiring differences,
	// otherwise 0. It starts at 0, stays set until a successful check finds no drift, and
	// is unchanged on read errors.
	ConfigDrift = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rwa_config_drift", Help: "1 if the last config-drift check found on-chain wiring that no longer matches the project record, 0 otherwise.",
	})

	// IdempotencyOutboxErrorsTotal counts failed IdempotencyRepository.Complete and
	// Release calls after the handler responds. Failures may leave Idempotency-Key
	// reservations pending, causing 409 retries until TTL expiry.
	IdempotencyOutboxErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_idempotency_outbox_errors_total", Help: "Idempotency store Complete/Release calls that failed after the handler already ran, by phase.",
	}, []string{"phase"})

	// ReadModelReconcileErrorsTotal counts failed reconciler ticks by name; repeated
	// errors, such as database failures, may stall convergence
	ReadModelReconcileErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rwa_read_model_reconcile_errors_total", Help: "Read-model reconciler ticks that returned an error, by reconciler name.",
	}, []string{"reconciler"})
)

// Handler returns the promhttp handler for /metrics on the metrics listener
func Handler() http.Handler {
	return promhttp.Handler()
}

// GinMiddleware records rwa_http_requests_total and rwa_http_request_duration_seconds
// for requests reaching it. Path labels use c.FullPath() route templates or "unmatched"
// to avoid per-ID label growth.
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched" // SPA fallback or 404 without a route template
		}
		status := strconv.Itoa(c.Writer.Status())
		httpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}
