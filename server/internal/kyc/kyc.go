// Package kyc is the server's pluggable KYC-provider abstraction: a single
// Provider interface backed by exactly one of three implementations, selected
// at startup by the KYC_PROVIDER config value (mirroring how internal/keys
// selects a hot-key backend by KEY_PROVIDER_MODE):
//
//   - "none":   the provider-agnostic default. There is no server-initiated
//     verification (StartVerification returns ErrStartNotSupported); the
//     webhook path verifies a plain HMAC-SHA256 over the raw body against the
//     shared KYC_WEBHOOK_HMAC_SECRET and expects the body to already be the
//     platform's generic decision shape. This is the historical behavior kept
//     for backward compatibility and for issuers wiring their own provider.
//   - "sumsub": Sum&Substance. StartVerification issues a WebSDK access token
//     bound to the wallet address (externalUserId), so the address rides in the
//     provider's own webhook and needs no server-side lookup. VerifyWebhook
//     checks the X-Payload-Digest HMAC and maps reviewResult.reviewAnswer.
//   - "onfido": Onfido (Entrust). StartVerification creates an applicant + an
//     Onfido Studio workflow run + an SDK token; because Onfido's webhook
//     carries only the workflow-run id (no custom field), the subject wallet is
//     resolved by the caller through the KYCVerification binding written at
//     start time. VerifyWebhook checks the X-SHA2-Signature HMAC and maps the
//     workflow-run status.
//
// Every concrete Provider is a stateless HTTP + crypto adapter: it holds no
// database handle and never touches persistence. Subject binding, replay
// protection, and the on-chain relay all stay in internal/compliance +
// internal/api, exactly where they were before this package existed — a
// provider only (a) starts a verification and (b) turns one signed webhook
// delivery into a verified, provider-agnostic Decision.
package kyc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Mode selects which backend New constructs.
type Mode string

const (
	ModeNone   Mode = "none"
	ModeSumsub Mode = "sumsub"
	ModeOnfido Mode = "onfido"

	defaultModeIfEmpty = ModeNone
)

var (
	// ErrInvalidSignature is returned by VerifyWebhook when the provider's
	// signature header does not verify against the configured secret. The API
	// layer maps it to 401.
	ErrInvalidSignature = errors.New("kyc: webhook signature verification failed")

	// ErrStartNotSupported is returned by StartVerification for a provider that
	// cannot begin a verification server-side (the "none" provider). The API
	// layer maps it to 501.
	ErrStartNotSupported = errors.New("kyc: verification initiation is not available for the configured provider")

	// ErrUnhandledEvent is returned by VerifyWebhook for a correctly signed
	// delivery that carries no decision this platform acts on (e.g. an Onfido
	// report.completed or a non-terminal workflow status). The API layer
	// acknowledges it with 202 so the provider stops retrying, without
	// recording anything.
	ErrUnhandledEvent = errors.New("kyc: webhook carried no actionable decision")

	// ErrMissingTimestamp is returned by VerifyWebhook for a signed delivery
	// that carries a real decision but no provider timestamp to stamp it with
	// (for Sumsub, also nothing to build a stable event id from).
	//
	// internal/compliance uses Decision.OccurredAt both to age deliveries out
	// and to decide which of an address's decisions is newest, so a substituted
	// time.Now() would make the delivery unconditionally fresh AND
	// unconditionally the newest — letting a late redelivery of an old decision
	// overwrite a newer one. Providers do deliver out of order, so refuse.
	//
	// Separate from ErrUnhandledEvent because they mean different things to an
	// operator (nothing to do vs. a decision was dropped), even though the API
	// layer answers 202 to both — see internal/api's kycWebhook for why it
	// can't answer a 4xx.
	ErrMissingTimestamp = errors.New("kyc: webhook carries no usable provider decision timestamp")
)

// Session is what StartVerification returns for the investor SPA to launch the
// provider's verification flow. Exactly one of Token (an SDK/init token the
// embedded web SDK consumes) or URL (a hosted redirect flow) is set, depending
// on the provider. Ref is the provider-side reference bound to this
// verification — the caller persists a (provider, ref) -> address binding so a
// later webhook can be resolved back to the subject wallet.
type Session struct {
	Provider  string    `json:"provider"`
	Token     string    `json:"token,omitempty"`
	URL       string    `json:"url,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// Decision is a verified webhook outcome in the platform's provider-agnostic
// vocabulary, BEFORE the subject wallet is resolved. Address is set only when
// the provider embeds the subject in its own webhook (Sumsub's externalUserId);
// otherwise it is empty and Ref identifies the verification the caller resolves
// to an address via the KYCVerification binding. Status is one of "Allowed",
// "Blocked", or "Pending" (the same enum internal/compliance expects).
type Decision struct {
	Provider   string
	EventID    string
	Ref        string
	Address    string
	Status     string
	OccurredAt int64 // Unix seconds
	ValidUntil int64 // Unix seconds; 0 = no expiry
}

// Provider is the pluggable KYC backend. Implementations are stateless and
// safe for concurrent use.
type Provider interface {
	// Name is the stable provider identifier ("sumsub", "onfido", "generic"),
	// used as the KYCVerification.Provider key and in audit records.
	Name() string
	// StartVerification begins a verification for address and returns the
	// Session the SPA launches. Returns ErrStartNotSupported for a provider
	// with no server-initiated flow.
	StartVerification(ctx context.Context, address string) (Session, error)
	// VerifyWebhook authenticates one raw webhook delivery (signature in
	// headers) and maps it to a Decision. Returns ErrInvalidSignature on a bad
	// signature, ErrUnhandledEvent for a signed-but-irrelevant delivery, and
	// ErrMissingTimestamp for a decision with no usable provider timestamp. A
	// returned Decision always has OccurredAt > 0 taken from the provider's own
	// payload — implementations MUST NOT substitute the current time.
	VerifyWebhook(rawBody []byte, headers http.Header) (Decision, error)
}

// Config bundles every backend's configuration; only the fields the selected
// Mode needs are read (mirrors internal/keys.Config).
type Config struct {
	Mode Mode

	// GenericHMACSecret backs the "none" provider — the shared
	// KYC_WEBHOOK_HMAC_SECRET. Empty with Mode "none" means the KYC webhook
	// feature is off (New returns nil, nil).
	GenericHMACSecret string

	Sumsub SumsubConfig
	Onfido OnfidoConfig
}

// New resolves cfg.Mode to a Provider. An empty Mode defaults to "none".
// Returns (nil, nil) — not an error — when Mode is "none" and no generic
// webhook secret is configured: an absent KYC configuration means "the feature
// is disabled", not a startup failure, matching internal/keys.Load's treatment
// of an unconfigured hot key.
func New(cfg Config) (Provider, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = defaultModeIfEmpty
	}
	switch mode {
	case ModeNone:
		if cfg.GenericHMACSecret == "" {
			return nil, nil
		}
		return newGenericProvider(cfg.GenericHMACSecret), nil
	case ModeSumsub:
		return newSumsubProvider(cfg.Sumsub)
	case ModeOnfido:
		return newOnfidoProvider(cfg.Onfido)
	default:
		return nil, fmt.Errorf("kyc: unknown KYC_PROVIDER %q (want %q, %q, or %q)", mode, ModeNone, ModeSumsub, ModeOnfido)
	}
}
