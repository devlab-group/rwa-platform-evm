package kyc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SumsubConfig configures the Sum&Substance provider. AppToken + SecretKey
// authenticate our outbound API calls; WebhookSecret verifies inbound webhooks
// (a separate value set in the Sumsub dashboard's webhook settings). BaseURL
// and LevelName have sensible defaults.
type SumsubConfig struct {
	AppToken      string
	SecretKey     string
	WebhookSecret string
	BaseURL       string // default https://api.sumsub.com
	LevelName     string // verification level, default "basic-kyc-level"
}

const (
	sumsubDefaultBaseURL   = "https://api.sumsub.com"
	sumsubDefaultLevelName = "basic-kyc-level"
	sumsubTokenTTLSecs     = 600
	sumsubHTTPTimeout      = 15 * time.Second

	sumsubHeaderAppToken   = "X-App-Token"
	sumsubHeaderAccessTs   = "X-App-Access-Ts"
	sumsubHeaderAccessSig  = "X-App-Access-Sig"
	sumsubHeaderDigest     = "X-Payload-Digest"
	sumsubHeaderDigestAlgo = "X-Payload-Digest-Alg"
)

type sumsubProvider struct {
	cfg    SumsubConfig
	client *http.Client
}

func newSumsubProvider(cfg SumsubConfig) (Provider, error) {
	if cfg.AppToken == "" || cfg.SecretKey == "" {
		return nil, errors.New("kyc: sumsub requires app_token and secret_key")
	}
	if cfg.WebhookSecret == "" {
		return nil, errors.New("kyc: sumsub requires webhook_secret")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = sumsubDefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.LevelName == "" {
		cfg.LevelName = sumsubDefaultLevelName
	}
	return &sumsubProvider{cfg: cfg, client: &http.Client{Timeout: sumsubHTTPTimeout}}, nil
}

func (p *sumsubProvider) Name() string { return "sumsub" }

// StartVerification requests a WebSDK access token bound to the wallet address
// as externalUserId. Sumsub auto-creates the applicant on first use of the
// token, and — critically — echoes that externalUserId back in every webhook,
// so the subject address rides in the signed webhook and needs no server-side
// lookup.
func (p *sumsubProvider) StartVerification(ctx context.Context, address string) (Session, error) {
	path := "/resources/accessTokens?" + url.Values{
		"userId":    {address},
		"levelName": {p.cfg.LevelName},
		"ttlInSecs": {strconv.Itoa(sumsubTokenTTLSecs)},
	}.Encode()

	var out struct {
		Token  string `json:"token"`
		UserID string `json:"userId"`
	}
	if err := p.do(ctx, http.MethodPost, path, nil, &out); err != nil {
		return Session{}, err
	}
	if out.Token == "" {
		return Session{}, errors.New("kyc: sumsub returned an empty access token")
	}
	return Session{
		Provider:  p.Name(),
		Token:     out.Token,
		Ref:       address, // Sumsub externalUserId == wallet address
		ExpiresAt: time.Now().Add(sumsubTokenTTLSecs * time.Second).UTC(),
	}, nil
}

// do performs a signed Sumsub API call. The signature is
// HMAC-SHA256(secretKey, ts + METHOD + path + body) as lowercase hex, where
// path includes the query string and body is the exact bytes sent.
func (p *sumsubProvider) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(p.cfg.SecretKey))
	mac.Write([]byte(ts))
	mac.Write([]byte(method))
	mac.Write([]byte(path))
	mac.Write(body)
	req.Header.Set(sumsubHeaderAppToken, p.cfg.AppToken)
	req.Header.Set(sumsubHeaderAccessTs, ts)
	req.Header.Set(sumsubHeaderAccessSig, hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kyc: sumsub %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// sumsubWebhook is the subset of an applicant webhook payload we consume.
type sumsubWebhook struct {
	ApplicantID    string `json:"applicantId"`
	InspectionID   string `json:"inspectionId"`
	CorrelationID  string `json:"correlationId"`
	ExternalUserID string `json:"externalUserId"`
	Type           string `json:"type"`
	ReviewStatus   string `json:"reviewStatus"`
	ReviewResult   struct {
		ReviewAnswer     string `json:"reviewAnswer"`     // GREEN | RED
		ReviewRejectType string `json:"reviewRejectType"` // FINAL | RETRY
	} `json:"reviewResult"`
	CreatedAtMs string `json:"createdAtMs"`
	CreatedAt   string `json:"createdAt"`
}

func (p *sumsubProvider) VerifyWebhook(rawBody []byte, headers http.Header) (Decision, error) {
	if !p.verifyDigest(rawBody, headers) {
		return Decision{}, ErrInvalidSignature
	}
	var wh sumsubWebhook
	if err := json.Unmarshal(rawBody, &wh); err != nil {
		return Decision{}, err
	}
	if wh.ExternalUserID == "" {
		// Without the externalUserId we set at start time, there is no subject
		// to bind the decision to — treat as an event we don't act on rather
		// than fabricate one.
		return Decision{}, ErrUnhandledEvent
	}

	status := "Pending"
	if wh.Type == "applicantReviewed" {
		switch strings.ToUpper(wh.ReviewResult.ReviewAnswer) {
		case "GREEN":
			status = "Allowed"
		case "RED":
			// A RETRY rejection is recoverable (the applicant can resubmit), so
			// it is not a terminal on-chain block — only a FINAL rejection is.
			if strings.EqualFold(wh.ReviewResult.ReviewRejectType, "FINAL") {
				status = "Blocked"
			}
		}
	}

	return Decision{
		Provider:   p.Name(),
		EventID:    sumsubEventID(wh),
		Ref:        wh.ExternalUserID,
		Address:    wh.ExternalUserID, // Sumsub carries the subject wallet directly
		Status:     status,
		OccurredAt: sumsubOccurredAt(wh),
	}, nil
}

// verifyDigest checks the X-Payload-Digest header against the configured
// webhook secret, honoring the algorithm named in X-Payload-Digest-Alg
// (defaulting to HMAC_SHA256_HEX).
func (p *sumsubProvider) verifyDigest(body []byte, headers http.Header) bool {
	var newHash func() hash.Hash
	switch strings.ToUpper(headers.Get(sumsubHeaderDigestAlgo)) {
	case "", "HMAC_SHA256_HEX":
		newHash = sha256.New
	case "HMAC_SHA1_HEX":
		newHash = sha1.New
	case "HMAC_SHA512_HEX":
		newHash = sha512.New
	default:
		return false
	}
	mac := hmac.New(newHash, []byte(p.cfg.WebhookSecret))
	mac.Write(body)
	expected := mac.Sum(nil)
	given, err := hex.DecodeString(strings.TrimSpace(headers.Get(sumsubHeaderDigest)))
	if err != nil {
		return false
	}
	return hmac.Equal(expected, given)
}

// sumsubEventID derives a stable-per-decision identity. Sumsub has no single
// guaranteed unique delivery id, so prefer correlationId when present and
// otherwise combine the applicant id, event type, and creation timestamp — a
// later re-review of the same applicant is a genuinely new decision (different
// createdAtMs) and must not be deduped against the earlier one.
func sumsubEventID(wh sumsubWebhook) string {
	if wh.CorrelationID != "" {
		return wh.CorrelationID
	}
	ts := wh.CreatedAtMs
	if ts == "" {
		ts = wh.CreatedAt
	}
	return strings.Join([]string{wh.ApplicantID, wh.Type, wh.ReviewStatus, ts}, "|")
}

// sumsubOccurredAt extracts the provider's decision timestamp (Unix seconds),
// preferring the millisecond epoch field and falling back to the RFC-style
// createdAt, then to now.
func sumsubOccurredAt(wh sumsubWebhook) int64 {
	if wh.CreatedAtMs != "" {
		if ms, err := strconv.ParseInt(wh.CreatedAtMs, 10, 64); err == nil {
			return ms / 1000
		}
	}
	if wh.CreatedAt != "" {
		// Sumsub renders createdAt like "2020-02-21 13:23:19+0000".
		if t, err := time.Parse("2006-01-02 15:04:05-0700", wh.CreatedAt); err == nil {
			return t.Unix()
		}
	}
	return time.Now().Unix()
}
