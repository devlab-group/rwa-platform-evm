package kyc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// GenericWebhookSignatureHeader carries none-mode raw-body HMAC-SHA256 as optional
// 0x-prefixed hex; see api/openapi.yaml's kycWebhook
const GenericWebhookSignatureHeader = "X-Webhook-Signature"

// genericProvider accepts the platform decision shape from issuer integrations using
// shared-secret HMAC authentication. It has no server-initiated verification flow.
type genericProvider struct {
	secret string
}

func newGenericProvider(secret string) *genericProvider {
	return &genericProvider{secret: secret}
}

func (p *genericProvider) Name() string { return "generic" }

// StartVerification is unsupported: the generic provider has no upstream API,
// and the issuer arranges verification itself
func (p *genericProvider) StartVerification(ctx context.Context, address string) (Session, error) {
	return Session{}, ErrStartNotSupported
}

// genericPayload mirrors compliance.WebhookPayload without importing compliance
type genericPayload struct {
	EventID    string `json:"eventId"`
	Address    string `json:"address"`
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	OccurredAt int64  `json:"occurredAt"`
	ValidUntil int64  `json:"validUntil"`
}

func (p *genericProvider) VerifyWebhook(rawBody []byte, headers http.Header) (Decision, error) {
	sig := headers.Get(GenericWebhookSignatureHeader)
	if !verifyHexHMACSHA256(rawBody, sig, p.secret) {
		return Decision{}, ErrInvalidSignature
	}
	var gp genericPayload
	if err := json.Unmarshal(rawBody, &gp); err != nil {
		return Decision{}, err
	}
	provider := gp.Provider
	if provider == "" {
		provider = p.Name()
	}
	return Decision{
		Provider:   provider,
		EventID:    gp.EventID,
		Ref:        gp.Address,
		Address:    gp.Address,
		Status:     gp.Status,
		OccurredAt: gp.OccurredAt,
		ValidUntil: gp.ValidUntil,
	}, nil
}

// verifyHexHMACSHA256 checks optionally 0x-prefixed hex HMAC-SHA256 in constant time.
// Empty secrets fail to prevent forgery. Generic and Onfido webhooks use it; Sumsub has
// its own verifier.
func verifyHexHMACSHA256(body []byte, sigHex, secret string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	given, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sigHex), "0x"))
	if err != nil {
		return false
	}
	return hmac.Equal(expected, given)
}
