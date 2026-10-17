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

// OnfidoConfig supplies API and webhook credentials, Studio WorkflowID, and API region
type OnfidoConfig struct {
	APIToken     string
	WebhookToken string
	WorkflowID   string
	Region       string // eu | us | ca (default eu)
	Referrer     string // SDK token referrer pattern, default "*://*/*"
	BaseURL      string // API host override for fake-provider tests
}

const (
	onfidoHTTPTimeout    = 15 * time.Second
	onfidoDefaultReferer = "*://*/*"
	// onfidoSigHeader carries the raw-body HMAC-SHA256 under WebhookToken
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
	if cfg.BaseURL != "" {
		base = strings.TrimRight(cfg.BaseURL, "/")
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

// StartVerification creates an applicant, workflow and SDK token; the caller binds
// returned workflow Ref to the wallet
func (p *onfidoProvider) StartVerification(ctx context.Context, address string) (Session, error) {
	// Use placeholder identity plus wallet address for dashboard tracing; verification
	// captures the applicant's identity
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
		Ref:      run.ID, // workflow-run ID carried by the webhook
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

// onfidoWebhook contains the delivery fields used by this adapter
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

	// Watchlist monitoring reports a changed match against a monitor, not a wallet
	if wh.Payload.Action == "watchlist_monitor.matches_updated" {
		return Decision{}, fmt.Errorf("%w: onfido watchlist monitor %s reported changed matches %s", ErrMonitoringSignal, obj.ID, obj.Href)
	}

	// Only approved or declined workflow-run deliveries produce a status decision
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
		// Ignore nonterminal or unsupported workflow statuses
		return Decision{}, ErrUnhandledEvent
	}

	occurredAt, err := onfidoOccurredAt(obj.CompletedAt)
	if err != nil {
		return Decision{}, err
	}

	return Decision{
		Provider:   p.Name(),
		EventID:    strings.Join([]string{wh.Payload.Action, obj.ID, obj.Status}, "|"),
		Ref:        obj.ID, // workflow-run id, resolved to the wallet via the KYCVerification binding
		Status:     status,
		OccurredAt: occurredAt,
	}, nil
}

// onfidoOccurredAt requires a parseable workflow completion timestamp,
// preserving provider ordering instead of substituting local time
func onfidoOccurredAt(iso string) (int64, error) {
	if iso == "" {
		return 0, fmt.Errorf("%w: onfido workflow run carries no completed_at_iso8601", ErrMissingTimestamp)
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0, fmt.Errorf("%w: onfido completed_at_iso8601 %q is not a valid RFC3339 timestamp", ErrMissingTimestamp, iso)
	}
	return t.Unix(), nil
}
