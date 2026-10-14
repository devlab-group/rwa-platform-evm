// Package kyc authenticates provider webhooks and starts verification flows.
// KYC_PROVIDER selects one adapter:
//
//   - "none": generic HMAC-SHA256 decisions; no server-initiated verification
//   - "sumsub": WebSDK tokens bound to externalUserId; X-Payload-Digest webhooks
//   - "onfido": applicant, Studio workflow and SDK token; X-SHA2-Signature webhooks
//
// Adapters hold no database handle. API and compliance services own wallet bindings,
// replay checks, decision persistence and on-chain relay.
package kyc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Mode selects the adapter constructed by New
type Mode string

const (
	ModeNone   Mode = "none"
	ModeSumsub Mode = "sumsub"
	ModeOnfido Mode = "onfido"

	defaultModeIfEmpty = ModeNone
)

var (
	// ErrInvalidSignature reports failed webhook authentication; the API returns 401
	ErrInvalidSignature = errors.New("kyc: webhook signature verification failed")

	// ErrStartNotSupported means the adapter has no server-initiated flow; the API returns 501
	ErrStartNotSupported = errors.New("kyc: verification initiation is not available for the configured provider")

	// ErrUnhandledEvent marks a signed delivery without an actionable decision;
	// it is acknowledged with 202 without being recorded
	ErrUnhandledEvent = errors.New("kyc: webhook carried no actionable decision")

	// ErrMissingTimestamp marks a decision without a usable provider timestamp.
	// Substituting local time would defeat freshness and ordering of late deliveries.
	// The API drops and audits it with 202, distinct from an irrelevant event.
	ErrMissingTimestamp = errors.New("kyc: webhook carries no usable provider decision timestamp")

	// ErrMonitoringSignal carries a signed monitoring reference without a wallet binding.
	// The API emits kyc_monitoring_hit and returns 202 for manual provider-dashboard review.
	ErrMonitoringSignal = errors.New("kyc: monitoring signal with no wallet in the delivery")
)

// Session supplies the token or hosted URL used to launch verification.
// The caller persists Ref with its provider and wallet for later webhook resolution.
type Session struct {
	Provider  string    `json:"provider"`
	Token     string    `json:"token,omitempty"`
	URL       string    `json:"url,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// Decision is an authenticated provider outcome before wallet resolution.
// Address is populated when the provider includes it; otherwise Ref resolves through
// a persisted verification binding. Status is Allowed, Blocked or Pending.
type Decision struct {
	Provider   string
	EventID    string
	Ref        string
	Address    string
	Status     string
	OccurredAt int64 // Unix seconds
	ValidUntil int64 // Unix seconds; 0 = no expiry
	// MonitoringHit describes a re-check requiring manual review; empty means no signal.
	// These Pending outcomes do not relay an on-chain status change.
	MonitoringHit string
}

// Provider implementations support concurrent verification and webhook calls
// without owning persistence
type Provider interface {
	// Name identifies the provider (sumsub, onfido or generic) in bindings and audits
	Name() string
	// StartVerification returns the session launched by the SPA, or ErrStartNotSupported
	StartVerification(ctx context.Context, address string) (Session, error)
	// VerifyWebhook checks the raw body and signature, then maps the delivery to a Decision.
	// Errors distinguish authentication, ignored events, missing timestamps and monitoring signals.
	// Timestamps MUST come from the provider payload; the API checks freshness and positivity.
	VerifyWebhook(rawBody []byte, headers http.Header) (Decision, error)
}

// Config supplies adapter settings; only the selected Mode's fields are used
type Config struct {
	Mode Mode

	// GenericHMACSecret authenticates the none-mode webhook; empty disables that provider
	GenericHMACSecret string

	Sumsub SumsubConfig
	Onfido OnfidoConfig
}

// New selects the configured adapter, defaulting an empty Mode to none.
// None mode without GenericHMACSecret returns nil, nil to disable KYC.
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
