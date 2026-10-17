package kyc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const startTestWallet = "0x1111111111111111111111111111111111111111"

type recordedRequest struct {
	Method, URI string
	Header      http.Header
	Body        []byte
}

// fakeProvider records every request and answers from replies, keyed by URL path
func fakeProvider(t *testing.T, replies map[string]string, failStatus int) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, recordedRequest{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Body: body})
		mu.Unlock()
		if failStatus != 0 {
			http.Error(w, "provider says no", failStatus)
			return
		}
		reply, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), got...)
	}
}

func newSumsubForTest(t *testing.T, baseURL string) Provider {
	t.Helper()
	p, err := New(Config{Mode: ModeSumsub, Sumsub: SumsubConfig{
		AppToken: "app-tok", SecretKey: "secret-key", WebhookSecret: "wh", BaseURL: baseURL, LevelName: "lvl-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSumsubStartVerification_SignsRequestAndReturnsToken(t *testing.T) {
	srv, requests := fakeProvider(t, map[string]string{"/resources/accessTokens": `{"token":"sdk-token-1","userId":"` + startTestWallet + `"}`}, 0)
	p := newSumsubForTest(t, srv.URL+"/")

	before := time.Now()
	sess, err := p.StartVerification(context.Background(), startTestWallet)
	if err != nil {
		t.Fatalf("StartVerification: %v", err)
	}
	after := time.Now()

	if sess.Provider != "sumsub" || sess.Token != "sdk-token-1" || sess.Ref != startTestWallet || sess.URL != "" {
		t.Errorf("session = %+v, want sumsub / sdk-token-1 / ref=wallet", sess)
	}
	if sess.ExpiresAt.Before(before.Add(600*time.Second).Add(-time.Second)) || sess.ExpiresAt.After(after.Add(600*time.Second).Add(time.Second)) {
		t.Errorf("ExpiresAt = %v, want about 600s after the call", sess.ExpiresAt)
	}

	reqs := requests()
	if len(reqs) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	wantURI := "/resources/accessTokens?levelName=lvl-1&ttlInSecs=600&userId=" + startTestWallet
	if r.Method != http.MethodPost || r.URI != wantURI {
		t.Errorf("request = %s %s, want POST %s", r.Method, r.URI, wantURI)
	}
	if len(r.Body) != 0 {
		t.Errorf("body = %q, want empty", r.Body)
	}
	if r.Header.Get("X-App-Token") != "app-tok" {
		t.Errorf("X-App-Token = %q, want app-tok", r.Header.Get("X-App-Token"))
	}
	ts := r.Header.Get("X-App-Access-Ts")
	if sec, err := strconv.ParseInt(ts, 10, 64); err != nil || sec < before.Unix()-1 || sec > after.Unix()+1 {
		t.Errorf("X-App-Access-Ts = %q, want the current unix time", ts)
	}
	// Signature covers ts + method + path with query + body, keyed by the secret
	mac := hmac.New(sha256.New, []byte("secret-key"))
	mac.Write([]byte(ts + "POST" + wantURI))
	if want := hex.EncodeToString(mac.Sum(nil)); r.Header.Get("X-App-Access-Sig") != want {
		t.Errorf("X-App-Access-Sig = %q, want %q", r.Header.Get("X-App-Access-Sig"), want)
	}
}

func TestSumsubStartVerification_ProviderErrorsSurface(t *testing.T) {
	tests := []struct {
		name    string
		replies map[string]string
		fail    int
		want    string
	}{
		{name: "non-2xx status", fail: http.StatusForbidden, want: "returned 403"},
		{name: "empty token", replies: map[string]string{"/resources/accessTokens": `{"token":""}`}, want: "empty access token"},
		{name: "malformed body", replies: map[string]string{"/resources/accessTokens": `not json`}, want: "invalid character"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, tt.replies, tt.fail)
			_, err := newSumsubForTest(t, srv.URL).StartVerification(context.Background(), startTestWallet)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("StartVerification error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func newOnfidoForTest(t *testing.T, baseURL string) Provider {
	t.Helper()
	cfg := onfidoTestConfig()
	cfg.BaseURL = baseURL
	p, err := New(Config{Mode: ModeOnfido, Onfido: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// onfidoTestConfig returns the Onfido credentials used by these tests
func onfidoTestConfig() OnfidoConfig {
	return OnfidoConfig{APIToken: "api-tok", WebhookToken: "wh-tok", WorkflowID: "wf-9", Region: "us", Referrer: "https://app.example/*"}
}

func TestOnfidoStartVerification_RunsApplicantWorkflowAndSDKToken(t *testing.T) {
	srv, requests := fakeProvider(t, map[string]string{
		"/applicants":    `{"id":"app-1"}`,
		"/workflow_runs": `{"id":"run-1"}`,
		"/sdk_token":     `{"token":"sdk-tok"}`,
	}, 0)
	p := newOnfidoForTest(t, srv.URL)

	sess, err := p.StartVerification(context.Background(), startTestWallet)
	if err != nil {
		t.Fatalf("StartVerification: %v", err)
	}
	if sess.Provider != "onfido" || sess.Token != "sdk-tok" || sess.Ref != "run-1" || !sess.ExpiresAt.IsZero() {
		t.Errorf("session = %+v, want onfido / sdk-tok / ref=run-1 / no expiry", sess)
	}

	reqs := requests()
	want := []struct{ uri, body string }{
		{"/applicants", `{"first_name":"RWA","last_name":"` + startTestWallet + `"}`},
		{"/workflow_runs", `{"applicant_id":"app-1","workflow_id":"wf-9"}`},
		{"/sdk_token", `{"applicant_id":"app-1","referrer":"https://app.example/*"}`},
	}
	if len(reqs) != len(want) {
		t.Fatalf("provider saw %d requests, want %d", len(reqs), len(want))
	}
	for i, w := range want {
		r := reqs[i]
		if r.Method != http.MethodPost || r.URI != w.uri {
			t.Errorf("request %d = %s %s, want POST %s", i, r.Method, r.URI, w.uri)
		}
		if string(r.Body) != w.body {
			t.Errorf("request %d body = %s, want %s", i, r.Body, w.body)
		}
		if got := r.Header.Get("Authorization"); got != "Token token=api-tok" {
			t.Errorf("request %d Authorization = %q, want %q", i, got, "Token token=api-tok")
		}
		var js map[string]any
		if err := json.Unmarshal(r.Body, &js); err != nil || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %d is not a JSON body with a JSON content type", i)
		}
	}
}

func TestOnfidoStartVerification_ProviderErrorsSurface(t *testing.T) {
	ok := map[string]string{"/applicants": `{"id":"app-1"}`, "/workflow_runs": `{"id":"run-1"}`, "/sdk_token": `{"token":"sdk-tok"}`}
	with := func(path, reply string) map[string]string {
		m := map[string]string{}
		for k, v := range ok {
			m[k] = v
		}
		m[path] = reply
		return m
	}
	tests := []struct {
		name    string
		replies map[string]string
		fail    int
		want    string
	}{
		{name: "non-2xx status", fail: http.StatusUnauthorized, want: "POST /applicants returned 401"},
		{name: "empty applicant id", replies: with("/applicants", `{"id":""}`), want: "empty applicant id"},
		{name: "empty workflow run id", replies: with("/workflow_runs", `{"id":""}`), want: "empty workflow run id"},
		{name: "empty sdk token", replies: with("/sdk_token", `{"token":""}`), want: "empty sdk token"},
		{name: "workflow step rejected", replies: map[string]string{"/applicants": `{"id":"app-1"}`}, want: "POST /workflow_runs returned 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, tt.replies, tt.fail)
			_, err := newOnfidoForTest(t, srv.URL).StartVerification(context.Background(), startTestWallet)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("StartVerification error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
