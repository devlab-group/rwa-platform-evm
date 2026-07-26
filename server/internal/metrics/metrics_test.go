package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
	// The route TEMPLATE, not the raw path with "42" in it, must appear —
	// otherwise every distinct redemption id would be its own label value.
	if !strings.Contains(body, `path="/api/v1/redemptions/:id"`) {
		t.Errorf("metrics output missing the route-template label; got:\n%s", excerptContaining(body, "rwa_http_requests_total"))
	}
	if strings.Contains(body, `path="/api/v1/redemptions/42"`) {
		t.Error("metrics output used the raw request path (with the id in it) instead of the route template — this would blow up cardinality")
	}
}

func TestBusinessGaugesAreSettable(t *testing.T) {
	SalesInventoryTokens.Set(1234.5)
	RedemptionsPendingCount.Set(3)
	RedemptionsPendingOldestAgeSeconds.Set(7200)
	RedemptionsFundedUnclaimedCount.Set(1)
	AlertsFiredTotal.WithLabelValues("pending_redemption_sla").Inc()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, want := range []string{
		"rwa_sales_inventory_tokens 1234.5",
		"rwa_redemptions_pending_count 3",
		"rwa_redemptions_pending_oldest_age_seconds 7200",
		"rwa_redemptions_funded_unclaimed_count 1",
		`rwa_alerts_fired_total{kind="pending_redemption_sla"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
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
