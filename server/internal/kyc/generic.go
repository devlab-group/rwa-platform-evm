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
// lowercase hex of HMAC-SHA256(rawBody, sharedSecret), optionally 0x-prefixed.
// Matches the historical KYC webhook contract (api/openapi.yaml's kycWebhook).
const GenericWebhookSignatureHeader = "X-Webhook-Signature"

// genericProvider is the provider-agnostic default: no server-initiated flow,
// and a webhook body that is already the platform's generic decision shape,
// authenticated by a shared HMAC secret. It preserves the exact pre-provider
// behavior for issuers who feed the webhook from their own integration.
type genericProvider struct {
	secret string
}

func newGenericProvider(secret string) *genericProvider {
	return &genericProvider{secret: secret}
}

func (p *genericProvider) Name() string { return "generic" }

// StartVerification is unsupported: the generic provider has no upstream API to
// create a verification against — the issuer arranges verification themselves.
func (p *genericProvider) StartVerification(ctx context.Context, address string) (Session, error) {
	return Session{}, ErrStartNotSupported
}

// genericPayload is the wire shape the generic webhook body carries — the same
// fields internal/compliance.WebhookPayload marshals, decoded here without
// importing that package so this adapter stays a leaf dependency.
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
// valid HMAC-SHA256 of body under secret, in constant time. An empty secret
// always fails (an empty HMAC key is publicly computable, so anyone could
// forge a signature) — the same defense-in-depth rule internal/compliance
// applies. Shared by every provider's webhook verification.
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
