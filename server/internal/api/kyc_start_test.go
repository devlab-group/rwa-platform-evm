package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/kyc"
)

const startKYCPath = "/api/v1/compliance/kyc/start"

// kycProviderStub answers like a KYC vendor and records the request paths it saw
type kycProviderStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
}

func newKYCProviderStub(t *testing.T, status int, replies map[string]string) *kycProviderStub {
	t.Helper()
	s := &kycProviderStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		s.hits = append(s.hits, r.URL.Path)
		s.mu.Unlock()
		if status != http.StatusOK {
			http.Error(w, "vendor outage", status)
			return
		}
		_, _ = io.WriteString(w, replies[r.URL.Path])
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *kycProviderStub) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...)
}

func sumsubAt(t *testing.T, base string) kyc.Provider {
	t.Helper()
	p, err := kyc.New(kyc.Config{Mode: kyc.ModeSumsub, Sumsub: kyc.SumsubConfig{AppToken: "t", SecretKey: "k", WebhookSecret: "w", BaseURL: base}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func walletSession(t *testing.T, env *testEnv, address string) map[string]string {
	t.Helper()
	token, _, err := env.app.Sessions.Issue(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"X-Wallet-Session": token}
}

func TestStartKYC_WithoutWalletSession_Is401AndNeverCallsProvider(t *testing.T) {
	env := setupTestApp(t)
	stub := newKYCProviderStub(t, http.StatusOK, nil)
	env.app.KYC = sumsubAt(t, stub.srv.URL)

	for name, headers := range map[string]map[string]string{
		"no header":     nil,
		"unknown token": {"X-Wallet-Session": "not-a-real-token"},
	} {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, env.router, http.MethodPost, startKYCPath, nil, headers)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
			}
		})
	}
	if got := stub.paths(); len(got) != 0 {
		t.Errorf("provider was called %v without a wallet session", got)
	}
}

func TestStartKYC_Sumsub_ReturnsTokenForSessionWalletOnly(t *testing.T) {
	env := setupTestApp(t)
	wallet := addr("0xB0B0")
	stub := newKYCProviderStub(t, http.StatusOK, map[string]string{"/resources/accessTokens": `{"token":"sumsub-sdk-token"}`})
	env.app.KYC = sumsubAt(t, stub.srv.URL)

	before := time.Now()
	// A body naming another wallet must not change the subject
	w := doJSON(t, env.router, http.MethodPost, startKYCPath, map[string]string{"address": addr("0xDEAD")}, walletSession(t, env, wallet))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got dto.KYCSession
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "sumsub" || got.Token != "sumsub-sdk-token" || got.Ref != wallet || got.URL != "" {
		t.Errorf("session = %+v, want sumsub token bound to ref=%s", got, wallet)
	}
	exp, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt %q is not RFC3339: %v", got.ExpiresAt, err)
	}
	if d := exp.Sub(before); d < 590*time.Second || d > 610*time.Second {
		t.Errorf("expiresAt is %v after the request, want about 600s", d)
	}

	v, err := env.app.Repos.KYCVerifications.GetByRef(context.Background(), "sumsub", wallet)
	if err != nil {
		t.Fatalf("provider reference was not stored: %v", err)
	}
	if v.Address != wallet {
		t.Errorf("stored binding address = %s, want %s", v.Address, wallet)
	}
}

func TestStartKYC_Onfido_StoresWorkflowRunBindingAndOmitsExpiry(t *testing.T) {
	env := setupTestApp(t)
	wallet := addr("0xB0B0")
	stub := newKYCProviderStub(t, http.StatusOK, map[string]string{
		"/v3.6/applicants": `{"id":"app-1"}`, "/v3.6/workflow_runs": `{"id":"run-77"}`, "/v3.6/sdk_token": `{"token":"onfido-sdk-token"}`,
	})
	onfido, err := kyc.New(kyc.Config{Mode: kyc.ModeOnfido, Onfido: kyc.OnfidoConfig{APIToken: "t", WebhookToken: "w", WorkflowID: "wf", Region: "eu", BaseURL: stub.srv.URL + "/v3.6"}})
	if err != nil {
		t.Fatal(err)
	}
	env.app.KYC = onfido

	w := doJSON(t, env.router, http.MethodPost, startKYCPath, nil, walletSession(t, env, wallet))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"provider": "onfido", "token": "onfido-sdk-token", "ref": "run-77"}
	if len(raw) != len(want) {
		t.Errorf("response = %v, want exactly %v (no expiresAt for a zero expiry)", raw, want)
	}
	for k, v := range want {
		if raw[k] != v {
			t.Errorf("response[%s] = %v, want %v", k, raw[k], v)
		}
	}
	v, err := env.app.Repos.KYCVerifications.GetByRef(context.Background(), "onfido", "run-77")
	if err != nil {
		t.Fatalf("workflow run binding was not stored: %v", err)
	}
	if v.Address != wallet {
		t.Errorf("binding address = %s, want %s", v.Address, wallet)
	}
}

func TestStartKYC_ProviderOutage_Is502AndStoresNoBinding(t *testing.T) {
	env := setupTestApp(t)
	wallet := addr("0xB0B0")
	stub := newKYCProviderStub(t, http.StatusServiceUnavailable, nil)
	env.app.KYC = sumsubAt(t, stub.srv.URL)

	w := doJSON(t, env.router, http.MethodPost, startKYCPath, nil, walletSession(t, env, wallet))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
	if _, err := env.app.Repos.KYCVerifications.GetByRef(context.Background(), "sumsub", wallet); err == nil {
		t.Error("a binding was stored for a failed verification start")
	}
	// The provider's path, status and response text stay in the server log
	var resp dto.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if resp.Code != CodeInternal || !strings.HasPrefix(resp.Message, "KYC provider unavailable (reference ") {
		t.Errorf("error = %+v, want code %q and a generic message with a log reference", resp, CodeInternal)
	}
	for _, leak := range []string{"vendor outage", "returned 503", "sumsub", "/resources/", wallet} {
		if strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(leak)) {
			t.Errorf("body = %s, leaks %q from the provider call", w.Body.String(), leak)
		}
	}
}

func TestStartKYC_Unsupported_Is501(t *testing.T) {
	env := setupTestApp(t) // default provider is the generic webhook-only one
	w := doJSON(t, env.router, http.MethodPost, startKYCPath, nil, walletSession(t, env, addr("0xB0B0")))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("generic provider: status = %d, want 501, body=%s", w.Code, w.Body.String())
	}

	env.app.KYC = nil
	w = doJSON(t, env.router, http.MethodPost, startKYCPath, nil, walletSession(t, env, addr("0xB0B0")))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("no provider: status = %d, want 501, body=%s", w.Code, w.Body.String())
	}
}
