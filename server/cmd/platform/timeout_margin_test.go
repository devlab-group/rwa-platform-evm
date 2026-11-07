package main

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/sales"
)

func TestTimeoutMarginWarnings(t *testing.T) {
	const hour = 3600
	cases := []struct {
		name                 string
		cutoff               uint64
		sla                  time.Duration
		purchase, redemption int64
		want                 []string
	}{
		{name: "both timeouts clear the margin", cutoff: 6 * hour, sla: 26 * time.Hour, purchase: 7 * 24 * hour, redemption: 14 * 24 * hour},
		{name: "purchase timeout inside the margin", cutoff: 6 * hour, sla: 26 * time.Hour, purchase: 24 * hour, redemption: 14 * 24 * hour, want: []string{"purchaseTimeout"}},
		{name: "a timeout equal to the margin is too short", cutoff: 6 * hour, sla: 26 * time.Hour, purchase: 32 * hour, redemption: 14 * 24 * hour, want: []string{"purchaseTimeout"}},
		{name: "one second above the margin", cutoff: 6 * hour, sla: 26 * time.Hour, purchase: 32*hour + 1, redemption: 14 * 24 * hour},
		{name: "both inside", cutoff: 6 * hour, sla: 26 * time.Hour, purchase: 24 * hour, redemption: 30 * hour, want: []string{"purchaseTimeout", "redemptionTimeout"}},
		{name: "an unset timeout is skipped", cutoff: 6 * hour, sla: 26 * time.Hour, purchase: 0, redemption: 14 * 24 * hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := timeoutMarginWarnings(tc.cutoff, tc.sla, tc.purchase, tc.redemption)
			if len(got) != len(tc.want) {
				t.Fatalf("warnings = %q, want one each for %v", got, tc.want)
			}
			for i, w := range tc.want {
				if !strings.HasPrefix(got[i], w+" ") {
					t.Errorf("warning %d = %q, want it to name %s", i, got[i], w)
				}
			}
		})
	}
}

// the warning needs the oracle's cut-off, a feed SLA and the stored timeouts, and is logged once
func TestEvaluateAlerts_WarnsOnceWhenATimeoutIsTooShort(t *testing.T) {
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(original) })
	timeoutWarned = sync.Map{}

	app, repos := guardTestApp(t, 0, nil)
	ctx := context.Background()
	if err := repos.Projects.Upsert(ctx, &models.Project{PurchaseTimeout: 24 * 3600, RedemptionTimeout: 14 * 24 * 3600}); err != nil {
		t.Fatal(err)
	}
	app.Sales = sales.New(oracleChain{oracleKind: true, cutoff: 6 * 3600}, alertVault, common.HexToAddress("0xB"), repos.Purchases)

	count := func() int { return strings.Count(logs.String(), "purchaseTimeout is 86400s") }
	evaluateAlerts(ctx, config.Config{PriceFeedSLA: 26 * time.Hour}, repos, app)
	evaluateAlerts(ctx, config.Config{PriceFeedSLA: 26 * time.Hour}, repos, app)
	if count() != 1 {
		t.Fatalf("purchaseTimeout warning logged %d times, want once:\n%s", count(), logs.String())
	}
	if strings.Contains(logs.String(), "redemptionTimeout is") {
		t.Errorf("redemptionTimeout warned although it clears the margin:\n%s", logs.String())
	}

	// without a feed SLA the server does not know the update gap, so it stays quiet
	logs.Reset()
	timeoutWarned = sync.Map{}
	evaluateAlerts(ctx, config.Config{}, repos, app)
	if count() != 0 {
		t.Errorf("warned without alerts.price_feed_sla:\n%s", logs.String())
	}
}
