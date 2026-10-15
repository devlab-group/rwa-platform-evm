package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/kyc"
)

func postStatus(t *testing.T, env *testEnv, address, status, key string) (int, dto.ErrorResponse) {
	t.Helper()
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/compliance/status",
		SetStatusRequestBody{Address: address, Status: status},
		map[string]string{"Authorization": env.bearer, "Idempotency-Key": key})
	var e dto.ErrorResponse
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	return w.Code, e
}

func seedAllowed(t *testing.T, env *testEnv, address string) {
	t.Helper()
	if err := env.app.Repos.Investors.Upsert(context.Background(), &models.Investor{Address: address, Status: models.ComplianceAllowed, OwnershipVerified: true}); err != nil {
		t.Fatal(err)
	}
}

func txCount(t *testing.T, env *testEnv) int {
	t.Helper()
	ops, err := env.app.Repos.ComplianceOperations.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(ops)
}

func TestSetStatusHolderCap(t *testing.T) {
	env := setupTestApp(t)
	env.app.Guard = compliance.NewGuard(env.app.Repos, 1, nil, nil)
	held, other := addr("0xA1"), addr("0xB2")
	seedAllowed(t, env, held)

	code, e := postStatus(t, env, other, "Allowed", "cap-new")
	if code != http.StatusConflict || e.Code != CodeHolderCapReached {
		t.Fatalf("new allow at cap: %d %+v", code, e)
	}
	if txCount(t, env) != 0 {
		t.Fatal("a refused allow must not submit a transaction")
	}
	if code, _ := postStatus(t, env, held, "Allowed", "cap-renew"); code != http.StatusAccepted {
		t.Fatalf("renewal: %d", code)
	}
	if code, _ := postStatus(t, env, other, "Blocked", "cap-block"); code != http.StatusAccepted {
		t.Fatalf("block at cap: %d", code)
	}
}

func TestSetStatusCountsInFlightAllow(t *testing.T) {
	env := setupTestApp(t)
	env.app.Guard = compliance.NewGuard(env.app.Repos, 1, nil, nil)
	if code, _ := postStatus(t, env, addr("0xA1"), "Allowed", "inflight-1"); code != http.StatusAccepted {
		t.Fatalf("first allow: %d", code)
	}
	// The read model has not seen the first allow yet
	if code, e := postStatus(t, env, addr("0xB2"), "Allowed", "inflight-2"); code != http.StatusConflict || e.Code != CodeHolderCapReached {
		t.Fatalf("second allow: %d %+v", code, e)
	}
}

func TestSetStatusSanctionedAddress(t *testing.T) {
	env := setupTestApp(t)
	path := filepath.Join(t.TempDir(), "list.txt")
	listed := addr("0xA1")
	if err := os.WriteFile(path, []byte(listed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := compliance.NewSanctionsList(path)
	if _, err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	env.app.Guard = compliance.NewGuard(env.app.Repos, 0, nil, l)

	code, e := postStatus(t, env, listed, "Allowed", "sanc-1")
	if code != http.StatusConflict || e.Code != CodeSanctionedAddress {
		t.Fatalf("listed allow: %d %+v", code, e)
	}
	if txCount(t, env) != 0 {
		t.Fatal("a refused allow must not submit a transaction")
	}
	if code, _ := postStatus(t, env, listed, "Blocked", "sanc-2"); code != http.StatusAccepted {
		t.Fatalf("block of a listed wallet: %d", code)
	}
	if code, _ := postStatus(t, env, addr("0xB2"), "Allowed", "sanc-3"); code != http.StatusAccepted {
		t.Fatalf("unlisted allow: %d", code)
	}
}

func sumsubHold(t *testing.T, env *testEnv, wallet string) {
	t.Helper()
	sumsub, err := kyc.New(kyc.Config{Mode: kyc.ModeSumsub, Sumsub: kyc.SumsubConfig{AppToken: "app", SecretKey: "sec", WebhookSecret: "whsecret"}})
	if err != nil {
		t.Fatal(err)
	}
	env.app.KYC = sumsub
	payload := []byte(`{"applicantId":"a1","externalUserId":"` + wallet + `","type":"applicantOnHold","createdAtMs":"` + nowMs() + `"}`)
	w := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/webhook", payload,
		map[string]string{"X-Payload-Digest": sign(payload, "whsecret")})
	if w.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestMonitoringBlock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		block     bool
		wantEvent bool
	}{{"block", true, true}, {"alert", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestApp(t)
			env.app.MonitoringBlock = tc.block
			wallet := addr("0xB0B0")
			seedAllowed(t, env, wallet)
			sumsubHold(t, env, wallet)

			events, _ := env.app.Repos.KYCEvents.List(context.Background())
			var blocked *models.KYCEvent
			for _, e := range events {
				if e.Outcome == "Blocked" {
					blocked = e
				}
			}
			if (blocked != nil) != tc.wantEvent {
				t.Fatalf("blocked decision recorded = %v, want %v (events %d)", blocked != nil, tc.wantEvent, len(events))
			}
			if got := monitoringAlerts(t, env); len(got) != 1 || got[0] != wallet {
				t.Fatalf("alerts = %v", got)
			}
			if !tc.wantEvent {
				return
			}
			if blocked.ApplyStatus != models.KYCApplyAccepted {
				t.Fatalf("apply status = %s, want Accepted", blocked.ApplyStatus)
			}
			rec := compliance.NewWebhookReconciler(env.app.Repos.KYCEvents, env.app.Repos.Transactions, env.app.Status)
			if err := rec.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			events, _ = env.app.Repos.KYCEvents.List(context.Background())
			for _, e := range events {
				if e.Outcome == "Blocked" && (e.ApplyStatus != models.KYCApplyApplying || e.TxID == "") {
					t.Fatalf("reconciler did not submit the block: %+v", e)
				}
			}
		})
	}
}

// Walletless Onfido monitoring stays alert-only in block mode
func TestMonitoringBlockOnfidoStaysAlertOnly(t *testing.T) {
	env := setupTestApp(t)
	env.app.MonitoringBlock = true
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
		t.Fatalf("status = %d", w.Code)
	}
	events, _ := env.app.Repos.KYCEvents.List(context.Background())
	if len(events) != 0 || len(monitoringAlerts(t, env)) != 1 {
		t.Fatalf("events=%d alerts=%v", len(events), monitoringAlerts(t, env))
	}
}

// The local guard admits one of two allows racing for the last slot
func TestSetStatus_ConcurrentAllowsForLastSlot_OnlyOneSucceeds(t *testing.T) {
	env := setupTestApp(t)
	env.app.Guard = compliance.NewGuard(env.app.Repos, 1, nil, nil)
	wallets := []string{addr("0xC1"), addr("0xC2")}

	start := make(chan struct{})
	codes := make(chan int, len(wallets))
	for i, w := range wallets {
		body, err := json.Marshal(SetStatusRequestBody{Address: w, Status: "Allowed"})
		if err != nil {
			t.Fatal(err)
		}
		key := "race-" + string(rune('a'+i))
		go func() {
			<-start
			rec := doJSONRaw(t, env.router, http.MethodPost, "/api/v1/compliance/status", body,
				map[string]string{"Authorization": env.bearer, "Idempotency-Key": key, "Content-Type": "application/json"})
			codes <- rec.Code
		}()
	}
	close(start)

	got := map[int]int{}
	for range wallets {
		got[<-codes]++
	}
	if got[http.StatusAccepted] != 1 || got[http.StatusConflict] != 1 {
		t.Fatalf("status codes = %v, want one 202 and one 409", got)
	}
	if n := txCount(t, env); n != 1 {
		t.Fatalf("submitted allow transactions = %d, want 1", n)
	}
}
