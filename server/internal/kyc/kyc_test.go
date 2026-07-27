package kyc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
)

func hexHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestNewSelectsProvider(t *testing.T) {
	// none + no secret -> disabled (nil, nil)
	p, err := New(Config{Mode: ModeNone})
	if err != nil || p != nil {
		t.Fatalf("none+empty: got (%v, %v), want (nil, nil)", p, err)
	}
	// empty mode defaults to none
	p, err = New(Config{})
	if err != nil || p != nil {
		t.Fatalf("empty mode: got (%v, %v), want (nil, nil)", p, err)
	}
	// none + secret -> generic
	p, err = New(Config{Mode: ModeNone, GenericHMACSecret: "s"})
	if err != nil || p == nil || p.Name() != "generic" {
		t.Fatalf("none+secret: got (%v, %v)", p, err)
	}
	// sumsub missing fields -> error
	if _, err := New(Config{Mode: ModeSumsub}); err == nil {
		t.Fatal("sumsub with no config should error")
	}
	// onfido missing fields -> error
	if _, err := New(Config{Mode: ModeOnfido}); err == nil {
		t.Fatal("onfido with no config should error")
	}
	// unknown mode -> error
	if _, err := New(Config{Mode: "bogus"}); err == nil {
		t.Fatal("unknown mode should error")
	}
}

func TestGenericProvider(t *testing.T) {
	p := newGenericProvider("topsecret")
	if _, err := p.StartVerification(context.TODO(), "0xabc"); !errors.Is(err, ErrStartNotSupported) {
		t.Fatalf("generic StartVerification err = %v, want ErrStartNotSupported", err)
	}
	body := []byte(`{"eventId":"e1","address":"0x1111111111111111111111111111111111111111","provider":"acme","status":"Allowed","occurredAt":1700000000}`)

	h := http.Header{}
	h.Set(GenericWebhookSignatureHeader, hexHMAC("topsecret", body))
	dec, err := p.VerifyWebhook(body, h)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if dec.Provider != "acme" || dec.Status != "Allowed" || dec.EventID != "e1" {
		t.Fatalf("decision mismatch: %+v", dec)
	}
	if dec.Address == "" || dec.Address != dec.Ref {
		t.Fatalf("generic must carry address (also as ref): %+v", dec)
	}

	// wrong signature
	bad := http.Header{}
	bad.Set(GenericWebhookSignatureHeader, hexHMAC("wrong", body))
	if _, err := p.VerifyWebhook(body, bad); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("bad sig err = %v, want ErrInvalidSignature", err)
	}
}

func newTestSumsub(t *testing.T) *sumsubProvider {
	t.Helper()
	p, err := newSumsubProvider(SumsubConfig{AppToken: "app", SecretKey: "sec", WebhookSecret: "whsecret"})
	if err != nil {
		t.Fatal(err)
	}
	return p.(*sumsubProvider)
}

func TestSumsubWebhookMapping(t *testing.T) {
	p := newTestSumsub(t)
	addr := "0x1111111111111111111111111111111111111111"

	cases := []struct {
		name       string
		body       string
		wantStatus string
	}{
		{"approved", `{"applicantId":"a1","externalUserId":"` + addr + `","type":"applicantReviewed","reviewResult":{"reviewAnswer":"GREEN"},"createdAtMs":"1700000000000"}`, "Allowed"},
		{"final reject", `{"applicantId":"a1","externalUserId":"` + addr + `","type":"applicantReviewed","reviewResult":{"reviewAnswer":"RED","reviewRejectType":"FINAL"},"createdAtMs":"1700000000000"}`, "Blocked"},
		{"retry reject", `{"applicantId":"a1","externalUserId":"` + addr + `","type":"applicantReviewed","reviewResult":{"reviewAnswer":"RED","reviewRejectType":"RETRY"},"createdAtMs":"1700000000000"}`, "Pending"},
		{"pending type", `{"applicantId":"a1","externalUserId":"` + addr + `","type":"applicantPending","createdAtMs":"1700000000000"}`, "Pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			h := http.Header{}
			h.Set(sumsubHeaderDigest, hexHMAC("whsecret", body))
			dec, err := p.VerifyWebhook(body, h)
			if err != nil {
				t.Fatalf("VerifyWebhook: %v", err)
			}
			if dec.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", dec.Status, tc.wantStatus)
			}
			if dec.Address != addr || dec.Ref != addr {
				t.Fatalf("sumsub must carry externalUserId as address+ref: %+v", dec)
			}
			if dec.OccurredAt != 1700000000 {
				t.Fatalf("occurredAt = %d, want 1700000000", dec.OccurredAt)
			}
			if dec.Provider != "sumsub" {
				t.Fatalf("provider = %q", dec.Provider)
			}
		})
	}
}

func TestSumsubWebhookBadSignature(t *testing.T) {
	p := newTestSumsub(t)
	body := []byte(`{"applicantId":"a1","externalUserId":"0x1111111111111111111111111111111111111111","type":"applicantReviewed","reviewResult":{"reviewAnswer":"GREEN"}}`)
	h := http.Header{}
	h.Set(sumsubHeaderDigest, hexHMAC("not-the-secret", body))
	if _, err := p.VerifyWebhook(body, h); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
	// missing externalUserId -> unhandled
	body2 := []byte(`{"applicantId":"a1","type":"applicantReviewed","reviewResult":{"reviewAnswer":"GREEN"}}`)
	h2 := http.Header{}
	h2.Set(sumsubHeaderDigest, hexHMAC("whsecret", body2))
	if _, err := p.VerifyWebhook(body2, h2); !errors.Is(err, ErrUnhandledEvent) {
		t.Fatalf("err = %v, want ErrUnhandledEvent", err)
	}
}

func newTestOnfido(t *testing.T) *onfidoProvider {
	t.Helper()
	p, err := newOnfidoProvider(OnfidoConfig{APIToken: "tok", WebhookToken: "whtok", WorkflowID: "wf1", Region: "eu"})
	if err != nil {
		t.Fatal(err)
	}
	return p.(*onfidoProvider)
}

func TestOnfidoWebhookMapping(t *testing.T) {
	p := newTestOnfido(t)
	cases := []struct {
		name       string
		status     string
		wantErr    error
		wantStatus string
	}{
		{"approved", "approved", nil, "Allowed"},
		{"declined", "declined", nil, "Blocked"},
		{"review", "review", ErrUnhandledEvent, ""},
		{"abandoned", "abandoned", ErrUnhandledEvent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"payload":{"resource_type":"workflow_run","action":"workflow_run.completed","object":{"id":"run-123","status":"` + tc.status + `","completed_at_iso8601":"2023-01-02T03:04:05Z"}}}`)
			h := http.Header{}
			h.Set(onfidoSigHeader, hexHMAC("whtok", body))
			dec, err := p.VerifyWebhook(body, h)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyWebhook: %v", err)
			}
			if dec.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", dec.Status, tc.wantStatus)
			}
			if dec.Ref != "run-123" {
				t.Fatalf("ref = %q, want run-123 (workflow-run id)", dec.Ref)
			}
			if dec.Address != "" {
				t.Fatalf("onfido must NOT carry an address (resolved via store): %+v", dec)
			}
			if dec.Provider != "onfido" {
				t.Fatalf("provider = %q", dec.Provider)
			}
		})
	}
}

func TestOnfidoWebhookBadSignatureAndUnhandled(t *testing.T) {
	p := newTestOnfido(t)
	body := []byte(`{"payload":{"resource_type":"workflow_run","action":"workflow_run.completed","object":{"id":"run-123","status":"approved"}}}`)
	bad := http.Header{}
	bad.Set(onfidoSigHeader, hexHMAC("nope", body))
	if _, err := p.VerifyWebhook(body, bad); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
	// non-workflow event -> unhandled
	other := []byte(`{"payload":{"resource_type":"check","action":"check.completed","object":{"id":"c1","status":"complete"}}}`)
	h := http.Header{}
	h.Set(onfidoSigHeader, hexHMAC("whtok", other))
	if _, err := p.VerifyWebhook(other, h); !errors.Is(err, ErrUnhandledEvent) {
		t.Fatalf("err = %v, want ErrUnhandledEvent", err)
	}
}

func TestOnfidoBaseURL(t *testing.T) {
	for region, want := range map[string]string{
		"eu": "https://api.eu.onfido.com/v3.6",
		"us": "https://api.us.onfido.com/v3.6",
		"ca": "https://api.ca.onfido.com/v3.6",
		"":   "https://api.eu.onfido.com/v3.6",
	} {
		got, err := onfidoBaseURL(region)
		if err != nil || got != want {
			t.Fatalf("region %q: got (%q, %v), want %q", region, got, err, want)
		}
	}
	if _, err := onfidoBaseURL("mars"); err == nil {
		t.Fatal("bad region should error")
	}
}
