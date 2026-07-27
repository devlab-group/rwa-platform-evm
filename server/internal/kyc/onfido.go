package kyc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OnfidoConfig configures the Onfido (Entrust) provider. APIToken authenticates
// outbound API calls; WebhookToken verifies inbound webhooks (the token shown
// when you register the webhook in the Onfido dashboard). WorkflowID is the
// Onfido Studio workflow the verification runs. Region selects the data
// residency / API host.
type OnfidoConfig struct {
	APIToken     string
	WebhookToken string
	WorkflowID   string
	Region       string // eu | us | ca (default eu)
	Referrer     string // SDK token referrer pattern, default "*://*/*"
}

const (
	onfidoHTTPTimeout    = 15 * time.Second
	onfidoDefaultReferer = "*://*/*"
	// onfidoSigHeader carries the hex HMAC-SHA256 of the raw body under the
	// webhook token.
	onfidoSigHeader = "X-Sha2-Signature"
)

type onfidoProvider struct {
	cfg     OnfidoConfig
	baseURL string
	client  *http.Client
}

func newOnfidoProvider(cfg OnfidoConfig) (Provider, error) {
	if cfg.APIToken == "" || cfg.WebhookToken == "" {
		return nil, errors.New("kyc: onfido requires api_token and webhook_token")
	}
	if cfg.WorkflowID == "" {
		return nil, errors.New("kyc: onfido requires workflow_id")
	}
	base, err := onfidoBaseURL(cfg.Region)
	if err != nil {
		return nil, err
	}
	if cfg.Referrer == "" {
		cfg.Referrer = onfidoDefaultReferer
	}
	return &onfidoProvider{cfg: cfg, baseURL: base, client: &http.Client{Timeout: onfidoHTTPTimeout}}, nil
}

func onfidoBaseURL(region string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", "eu":
		return "https://api.eu.onfido.com/v3.6", nil
	case "us":
		return "https://api.us.onfido.com/v3.6", nil
	case "ca":
		return "https://api.ca.onfido.com/v3.6", nil
	default:
		return "", fmt.Errorf("kyc: onfido region must be eu, us, or ca, got %q", region)
	}
}

func (p *onfidoProvider) Name() string { return "onfido" }

// StartVerification creates the applicant, an Onfido Studio workflow run, and
// an SDK token, then returns the token plus the workflow-run id as Ref. Onfido
// webhooks carry only that workflow-run id (no custom field), so the caller
// persists the (onfido, workflowRunId) -> address binding to resolve the
// subject later.
func (p *onfidoProvider) StartVerification(ctx context.Context, address string) (Session, error) {
	// Onfido requires a name on applicant creation; the real identity is
	// captured from the document/biometrics during the flow. We store a
	// placeholder plus the wallet address as the last name for operator
	// traceability in the Onfido dashboard.
	var applicant struct {
		ID string `json:"id"`
	}
	if err := p.do(ctx, http.MethodPost, "/applicants",
		map[string]string{"first_name": "RWA", "last_name": address}, &applicant); err != nil {
		return Session{}, err
	}
	if applicant.ID == "" {
		return Session{}, errors.New("kyc: onfido returned an empty applicant id")
	}

	var run struct {
		ID string `json:"id"`
	}
	if err := p.do(ctx, http.MethodPost, "/workflow_runs",
		map[string]string{"workflow_id": p.cfg.WorkflowID, "applicant_id": applicant.ID}, &run); err != nil {
		return Session{}, err
	}
	if run.ID == "" {
		return Session{}, errors.New("kyc: onfido returned an empty workflow run id")
	}

	var tok struct {
		Token string `json:"token"`
	}
	if err := p.do(ctx, http.MethodPost, "/sdk_token",
		map[string]string{"applicant_id": applicant.ID, "referrer": p.cfg.Referrer}, &tok); err != nil {
		return Session{}, err
	}
	if tok.Token == "" {
		return Session{}, errors.New("kyc: onfido returned an empty sdk token")
	}

	return Session{
		Provider: p.Name(),
		Token:    tok.Token,
		Ref:      run.ID, // workflow-run id — the identifier the webhook carries
	}, nil
}

func (p *onfidoProvider) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token token="+p.cfg.APIToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kyc: onfido %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// onfidoWebhook is the subset of a webhook payload we consume.
type onfidoWebhook struct {
	Payload struct {
		ResourceType string `json:"resource_type"`
		Action       string `json:"action"`
		Object       struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			CompletedAt string `json:"completed_at_iso8601"`
			Href        string `json:"href"`
			WorkflowID  string `json:"workflow_id"`
		} `json:"object"`
	} `json:"payload"`
}

func (p *onfidoProvider) VerifyWebhook(rawBody []byte, headers http.Header) (Decision, error) {
	if !verifyHexHMACSHA256(rawBody, headers.Get(onfidoSigHeader), p.cfg.WebhookToken) {
		return Decision{}, ErrInvalidSignature
	}
	var wh onfidoWebhook
	if err := json.Unmarshal(rawBody, &wh); err != nil {
		return Decision{}, err
	}
	obj := wh.Payload.Object

	// Only a completed workflow run with a terminal decision changes on-chain
	// state. Every other delivery (status_changed, non-terminal status, report
	// or check events) is acknowledged and ignored.
	if !strings.HasPrefix(wh.Payload.Action, "workflow_run.") || obj.ID == "" {
		return Decision{}, ErrUnhandledEvent
	}
	var status string
	switch strings.ToLower(obj.Status) {
	case "approved":
		status = "Allowed"
	case "declined":
		status = "Blocked"
	default:
		// review / abandoned / error / processing / awaiting_input: nothing to
		// apply on-chain.
		return Decision{}, ErrUnhandledEvent
	}

	return Decision{
		Provider:   p.Name(),
		EventID:    strings.Join([]string{wh.Payload.Action, obj.ID, obj.Status}, "|"),
		Ref:        obj.ID, // workflow-run id, resolved to the wallet via the KYCVerification binding
		Status:     status,
		OccurredAt: onfidoOccurredAt(obj.CompletedAt),
	}, nil
}

func onfidoOccurredAt(iso string) int64 {
	if iso != "" {
		if t, err := time.Parse(time.RFC3339, iso); err == nil {
			return t.Unix()
		}
	}
	return time.Now().Unix()
}
