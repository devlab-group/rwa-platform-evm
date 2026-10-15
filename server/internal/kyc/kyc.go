// Package kyc starts verification and authenticates webhooks. KYC_PROVIDER selects:
// - "none": generic HMAC-SHA256 decisions without initiation.
// - "sumsub": WebSDK tokens bound to externalUserId, with X-Payload-Digest webhooks.
// - "onfido": applicant, Studio workflow and SDK token, with X-SHA2-Signature webhooks.
// Adapters hold no database handles; API/compliance own wallet bindings, replay checks,
// persistence and on-chain relay.
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

	// ErrMissingTimestamp rejects decisions without usable provider time, preserving
	// freshness and ordering. The API drops/audits with 202, separately from irrelevant
	// events.
	ErrMissingTimestamp = errors.New("kyc: webhook carries no usable provider decision timestamp")

	// ErrMonitoringSignal carries a signed monitoring reference without a wallet binding.
	// The API emits kyc_monitoring_hit and returns 202 for manual provider-dashboard review.
	ErrMonitoringSignal = errors.New("kyc: monitoring signal with no wallet in the delivery")
)

// Session supplies an SDK token or hosted URL. The caller persists Ref/provider/wallet
// for webhook resolution.
type Session struct {
	Provider  string    `json:"provider"`
	Token     string    `json:"token,omitempty"`
	URL       string    `json:"url,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// Decision holds an authenticated outcome before wallet resolution. Use Address when
// supplied, otherwise resolve Ref through the stored binding. Consumers accept Allowed,
// Blocked or Pending.
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
	// VerifyWebhook authenticates raw bytes and maps Decision, distinguishing invalid
	// signatures, ignored events, missing timestamps and monitoring signals. Timestamps
	// MUST come from provider payloads; compliance.ProcessDecision enforces freshness and
	// positivity.
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

// New defaults empty Mode to none; none without GenericHMACSecret returns (nil, nil),
// disabling KYC
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
