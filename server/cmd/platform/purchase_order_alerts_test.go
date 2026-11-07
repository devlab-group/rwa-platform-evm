package main

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/metrics"
	"github.com/rwa-platform/server/internal/sales"
)

func gaugeOf(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}

func TestPendingPurchaseOrders_FeedGaugesAndAlerts(t *testing.T) {
	ctx := context.Background()
	app, repos := guardTestApp(t, 0, nil)
	app.Sales = sales.New(brokenChain{}, alertVault, common.Address{}, repos.Purchases).WithOrders(repos.PurchaseOrders)
	now := time.Now()
	for _, o := range []models.PurchaseOrder{
		{ID: "stuck", Status: models.PurchaseOrderPending, CreatedAt: now.Add(-30 * time.Hour).Unix()},
		{ID: "fresh", Status: models.PurchaseOrderPending, CreatedAt: now.Add(-time.Hour).Unix()},
		{ID: "done", Status: models.PurchaseOrderSettled, CreatedAt: now.Add(-90 * time.Hour).Unix()},
	} {
		if err := repos.PurchaseOrders.Upsert(ctx, &o); err != nil {
			t.Fatal(err)
		}
	}

	refreshPurchaseOrderGauges(ctx, app)

	if got := gaugeOf(t, metrics.PurchaseOrdersPendingCount); got != 2 {
		t.Errorf("pending count = %v, want 2", got)
	}
	age := gaugeOf(t, metrics.PurchaseOrdersPendingOldestAgeSeconds)
	if want := (30 * time.Hour).Seconds(); age < want || age > want+60 {
		t.Errorf("oldest pending age = %vs, want about %vs (the closed order does not count)", age, want)
	}

	evaluateAlerts(ctx, config.Config{PendingPurchaseSLA: 24 * time.Hour}, repos, app)

	if got := alertTargets(t, repos, "pending_purchase_sla"); len(got) != 1 || got[0] != "stuck" {
		t.Errorf("pending_purchase_sla alerts = %v, want only the stuck order", got)
	}
}
