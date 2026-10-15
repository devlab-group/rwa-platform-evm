package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	dto "github.com/prometheus/client_model/go"

	"github.com/rwa-platform/server/internal/api"
	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/metrics"
)

var (
	gA     = common.HexToAddress("0x00000000000000000000000000000000000000a1").Hex()
	gB     = common.HexToAddress("0x00000000000000000000000000000000000000b2").Hex()
	gC     = common.HexToAddress("0x00000000000000000000000000000000000000c3").Hex()
	gD     = common.HexToAddress("0x00000000000000000000000000000000000000d4").Hex()
	gE     = common.HexToAddress("0x00000000000000000000000000000000000000e5").Hex()
	gF     = common.HexToAddress("0x00000000000000000000000000000000000000f6").Hex()
	gVault = common.HexToAddress("0x0000000000000000000000000000000000000099").Hex()
)

func guardTestApp(t *testing.T, maxWallets int, list *compliance.SanctionsList) (*api.App, *repository.Repositories) {
	t.Helper()
	repos := memory.New()
	app := &api.App{Repos: repos, Audit: auditlog.New(repos.AuditLogs)}
	app.Guard = compliance.NewGuard(repos, maxWallets, []string{gVault}, list)
	return app, repos
}

// gaugeValue reads rwa_allowed_wallets
func gaugeValue(t *testing.T) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.AllowedWallets.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetGauge().GetValue()
}

func seedInvestor(t *testing.T, repos *repository.Repositories, address string, status models.ComplianceStatus, validUntil int64) {
	t.Helper()
	if err := repos.Investors.Upsert(context.Background(), &models.Investor{Address: address, Status: status, ValidUntil: validUntil}); err != nil {
		t.Fatal(err)
	}
}

// seedMixedHolders adds active holders plus expired, blocked and system-wallet exclusions
func seedMixedHolders(t *testing.T, repos *repository.Repositories) {
	t.Helper()
	for _, a := range []string{gA, gB, gC} {
		seedInvestor(t, repos, a, models.ComplianceAllowed, 0)
	}
	seedInvestor(t, repos, gD, models.ComplianceAllowed, time.Now().Add(-time.Hour).Unix())
	seedInvestor(t, repos, gE, models.ComplianceBlocked, 0)
	seedInvestor(t, repos, gVault, models.ComplianceAllowed, 0)
}

// alertTargets returns sorted audit-log targets for kind
func alertTargets(t *testing.T, repos *repository.Repositories, kind string) []string {
	t.Helper()
	logs, err := repos.AuditLogs.List(context.Background(), "alerts", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range logs {
		if e.Action == "alerts."+kind {
			out = append(out, e.Target)
		}
	}
	sort.Strings(out)
	return out
}

func TestEvaluateAlerts_HolderCapExceeded(t *testing.T) {
	tests := []struct {
		name       string
		maxWallets int
		wantAlerts int
	}{
		{name: "three counted wallets above a cap of two", maxWallets: 2, wantAlerts: 1},
		{name: "at the cap, ignoring expired, blocked and system wallets", maxWallets: 3, wantAlerts: 0},
		{name: "no cap configured", maxWallets: 0, wantAlerts: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, repos := guardTestApp(t, tt.maxWallets, nil)
			seedMixedHolders(t, repos)

			evaluateAlerts(context.Background(), config.Config{}, repos, app)

			if got := len(alertTargets(t, repos, compliance.AlertHolderCapExceeded)); got != tt.wantAlerts {
				t.Fatalf("holder_cap_exceeded alerts = %d, want %d", got, tt.wantAlerts)
			}
		})
	}
}

func TestRefreshBusinessGauges_AllowedWalletsCountsOnlyAllowedUnexpiredNonSystem(t *testing.T) {
	app, repos := guardTestApp(t, 0, nil)
	seedMixedHolders(t, repos)

	refreshBusinessGauges(context.Background(), repos, app)
	if got := gaugeValue(t); got != 3 {
		t.Fatalf("rwa_allowed_wallets = %v, want 3", got)
	}

	seedInvestor(t, repos, gF, models.ComplianceAllowed, 0)
	refreshBusinessGauges(context.Background(), repos, app)
	if got := gaugeValue(t); got != 4 {
		t.Fatalf("rwa_allowed_wallets after one more allow = %v, want 4", got)
	}
}

func TestRescreenOnSanctionsReload_ReportsListedAllowedWalletsOnlyWhenTheListChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sanctions.txt")
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	write := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	app, repos := guardTestApp(t, 0, compliance.NewSanctionsList(path))
	seedMixedHolders(t, repos)
	ctx := context.Background()
	var seen []string
	newAlerts := func() []string {
		t.Helper()
		all := alertTargets(t, repos, compliance.AlertSanctionsMatch)
		// Return the multiset difference between sorted all and seen
		var added []string
		i := 0
		for _, a := range all {
			if i < len(seen) && seen[i] == a {
				i++
				continue
			}
			added = append(added, a)
		}
		seen = all
		return added
	}

	// Only the Allowed, unexpired wallet on the list is reported
	write("# test list\n"+gA+"\n"+gD+"\n"+gE+"\n", base)
	rescreenOnSanctionsReload(ctx, app)
	if got := newAlerts(); len(got) != 1 || got[0] != gA {
		t.Fatalf("first load alerts = %v, want [%s]", got, gA)
	}

	// An unchanged file raises nothing
	rescreenOnSanctionsReload(ctx, app)
	if got := newAlerts(); len(got) != 0 {
		t.Fatalf("unchanged file alerts = %v, want none", got)
	}

	// A changed file reports every listed Allowed wallet again
	write(gA+"\n"+gB+"\n", base.Add(time.Minute))
	rescreenOnSanctionsReload(ctx, app)
	got := newAlerts()
	sort.Strings(got)
	want := []string{gA, gB}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("changed file alerts = %v, want %v", got, want)
	}

	// A malformed reload preserves the list without raising match alerts
	write(gA+"\nnot-an-address\n", base.Add(2*time.Minute))
	rescreenOnSanctionsReload(ctx, app)
	if got := newAlerts(); len(got) != 0 {
		t.Fatalf("malformed file alerts = %v, want none", got)
	}
	if err := app.Guard.CheckAllow(ctx, gB, time.Now()); !errors.Is(err, compliance.ErrSanctionedAddress) {
		t.Fatalf("CheckAllow(%s) after a rejected reload = %v, want ErrSanctionedAddress from the kept list", gB, err)
	}
}
