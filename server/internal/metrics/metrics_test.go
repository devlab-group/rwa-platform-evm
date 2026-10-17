package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	dto "github.com/prometheus/client_model/go"
)

func init() { gin.SetMode(gin.TestMode) }

func TestGinMiddlewareRecordsRequestMetrics(t *testing.T) {
	r := gin.New()
	r.Use(GinMiddleware())
	r.GET("/api/v1/redemptions/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/redemptions/42", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	Handler().ServeHTTP(metricsRec, metricsReq)

	body := metricsRec.Body.String()
	// Use the route template, not the raw path containing "42", to avoid a label per
	// redemption ID
	if !strings.Contains(body, `path="/api/v1/redemptions/:id"`) {
		t.Errorf("metrics output missing the route-template label; got:\n%s", excerptContaining(body, "rwa_http_requests_total"))
	}
	if strings.Contains(body, `path="/api/v1/redemptions/42"`) {
		t.Error("metrics output used the raw request path (with the id in it) instead of the route template, which would multiply label values")
	}
}

func TestBusinessGaugesAreSettable(t *testing.T) {
	SalesInventoryTokens.Set(1234.5)
	RedemptionsPendingCount.Set(3)
	RedemptionsPendingOldestAgeSeconds.Set(7200)
	RedemptionsFundedUnclaimedCount.Set(1)
	// The counter is process-global; assert the delta so repeated runs stay green
	alerts := AlertsFiredTotal.WithLabelValues("pending_redemption_sla")
	before := counterValue(t, alerts)
	alerts.Inc()
	if got := counterValue(t, alerts) - before; got != 1 {
		t.Errorf("alerts counter delta = %v, want 1", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, want := range []string{
		"rwa_sales_inventory_tokens 1234.5",
		"rwa_redemptions_pending_count 3",
		"rwa_redemptions_pending_oldest_age_seconds 7200",
		"rwa_redemptions_funded_unclaimed_count 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

func counterValue(t *testing.T, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

func excerptContaining(body, marker string) string {
	idx := strings.Index(body, marker)
	if idx < 0 {
		return "(marker not found at all)"
	}
	end := idx + 300
	if end > len(body) {
		end = len(body)
	}
	return body[idx:end]
}
