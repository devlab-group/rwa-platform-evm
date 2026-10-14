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

// GenericWebhookSignatureHeader is the header the "none" provider reads: the
// hex HMAC-SHA256 of the raw body under the shared secret, optionally
// 0x-prefixed (see kycWebhook in api/openapi.yaml)
const GenericWebhookSignatureHeader = "X-Webhook-Signature"

// genericProvider is the provider-agnostic default for issuers who feed the
// webhook from their own integration. It has no server-initiated flow, and the
// webhook body is already the platform's decision shape, authenticated by a
// shared HMAC secret.
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

// genericPayload is the generic webhook body, with the fields of
// compliance.WebhookPayload. It is redeclared here so this package does not
// import internal/compliance.
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

// verifyHexHMACSHA256 reports whether sigHex (hex, optionally 0x-prefixed) is a
// valid HMAC-SHA256 of body under secret, compared in constant time. An empty
// secret always fails, because anyone can compute an HMAC under an empty key.
// Providers other than sumsub use it for webhook verification.
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
