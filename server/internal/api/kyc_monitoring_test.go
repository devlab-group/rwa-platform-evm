package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/kyc"
)

// nowMs supplies a fresh Sumsub timestamp for the fixture
func nowMs() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }

// monitoringAlerts extracts matching targets from the bounded audit fetch
func monitoringAlerts(t *testing.T, env *testEnv) []string {
	t.Helper()
	logs, err := env.app.Repos.AuditLogs.List(context.Background(), "alerts", 50)
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, e := range logs {
		if e.Action == "alerts.kyc_monitoring_hit" {
			targets = append(targets, e.Target)
		}
	}
	return targets
}

// An accepted Sumsub hold alerts for the projected Allowed wallet,
// but not unknown or blocked wallets
func TestKYCWebhookAlertsOnMonitoringHitForAllowedWallet(t *testing.T) {
	sumsub, err := kyc.New(kyc.Config{Mode: kyc.ModeSumsub, Sumsub: kyc.SumsubConfig{AppToken: "app", SecretKey: "sec", WebhookSecret: "whsecret"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		status    models.ComplianceStatus
		wantAlert bool
	}{
		"allowed wallet":    {models.ComplianceAllowed, true},
		"not yet onboarded": {models.ComplianceUnknown, false},
		"already blocked":   {models.ComplianceBlocked, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := setupTestApp(t)
			env.app.KYC = sumsub
			wallet := addr("0xB0B0")
			if err := env.app.Repos.Investors.Upsert(context.Background(), &models.Investor{Address: wallet, Status: tc.status, OwnershipVerified: true}); err != nil {
				t.Fatal(err)
			}

			payload := []byte(`{"applicantId":"a1","externalUserId":"` + wallet + `","type":"applicantOnHold","createdAtMs":"` + nowMs() + `"}`)
			w := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/webhook", payload,
				map[string]string{"X-Payload-Digest": sign(payload, "whsecret")})
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
			}
			got := monitoringAlerts(t, env)
			if tc.wantAlert && (len(got) != 1 || got[0] != wallet) {
				t.Fatalf("alerts = %v, want one for %s", got, wallet)
			}
			if !tc.wantAlert && len(got) != 0 {
				t.Fatalf("alerts = %v, want none", got)
			}
		})
	}
}

// A walletless Onfido signal alerts without creating a KYC decision record
func TestKYCWebhookAlertsOnOnfidoWatchlistSignal(t *testing.T) {
	env := setupTestApp(t)
	onfido, err := kyc.New(kyc.Config{
		Mode:   kyc.ModeOnfido,
		Onfido: kyc.OnfidoConfig{APIToken: "tok", WebhookToken: "whtok", WorkflowID: "wf1", Region: "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.app.KYC = onfido

	payload := []byte(`{"payload":{"resource_type":"watchlist_monitor","action":"watchlist_monitor.matches_updated","object":{"id":"wm-1"}}}`)
	w := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/webhook", payload,
		map[string]string{"X-SHA2-Signature": sign(payload, "whtok")})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", w.Code, w.Body.String())
	}
	if got := monitoringAlerts(t, env); len(got) != 1 || got[0] != "" {
		t.Fatalf("alerts = %v, want one with no wallet", got)
	}
	events, err := env.app.Repos.KYCEvents.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("recorded %d kyc events, want none", len(events))
	}
}
