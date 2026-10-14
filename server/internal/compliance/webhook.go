package compliance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// ErrInvalidSignature is returned when the webhook HMAC does not verify
var ErrInvalidSignature = errors.New("compliance: invalid webhook signature")

// ErrReplayed is returned for a delivery that was already processed, either a
// byte-for-byte resend (same payload hash) or the same (provider, eventId)
// resent with different bytes
var ErrReplayed = errors.New("compliance: webhook payload already processed (replay)")

// ErrStale is returned when a webhook's occurredAt is outside the delivery
// window, or when a newer decision for the same address already holds the
// claim
var ErrStale = errors.New("compliance: webhook decision is stale (outside the delivery window, or older than an already-applied decision)")

// ErrOwnershipNotVerified is returned when the webhook targets a wallet that
// has not completed the ownership challenge and allowManualOverride is not
// set
var ErrOwnershipNotVerified = errors.New("compliance: wallet has not proven ownership; requires a documented manual override")

const (
	// MaxWebhookAge is how old a delivery's occurredAt may be. Without a limit,
	// a correctly signed decision that was captured and held back would stay
	// valid indefinitely.
	MaxWebhookAge = 24 * time.Hour
	// MaxWebhookClockSkewAhead is how far occurredAt may be ahead of this
	// server's clock, to tolerate drift between it and the provider
	MaxWebhookClockSkewAhead = 5 * time.Minute
	// MaxValidUntilHorizon is how far in the future a validUntil may be
	MaxValidUntilHorizon = 100 * 365 * 24 * time.Hour // ~100 years
)

// WebhookPayload is the provider-agnostic KYC decision the platform processes.
// Provider, EventID and OccurredAt are required, because together they identify
// a delivery for replay and freshness checks.
type WebhookPayload struct {
	EventID  string `json:"eventId"`
	Address  string `json:"address"`
	Provider string `json:"provider"`
	Status   string `json:"status"` // "Allowed", "Blocked" or "Pending"
	// OccurredAt is the Unix time in seconds at which the provider made the
	// decision. The generic HMAC signature covers it with the rest of the
	// payload.
	OccurredAt int64 `json:"occurredAt"`
	ValidUntil int64 `json:"validUntil,omitempty"`
}

// VerifyHMAC reports whether signatureHex (hex, optionally "0x"-prefixed) is a
// valid HMAC-SHA256 of payload under secret, compared in constant time.
//
// An empty secret always fails. HMAC with an empty key is computable by anyone,
// so accepting it would let anyone forge a signature for an unconfigured
// webhook. config.Load also rejects missing and short secrets where it
// requires them (see MinWebhookSecretBytes), and this check covers every other
// caller.
func VerifyHMAC(payload []byte, signatureHex, secret string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	given, err := hex.DecodeString(strings.TrimPrefix(signatureHex, "0x"))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(expected, given) == 1
}

// WebhookService validates and records inbound KYC webhook deliveries
type WebhookService struct {
	events    repository.KYCEventRepository
	investors repository.InvestorRepository
	secret    string
}

// NewWebhookService constructs a WebhookService
func NewWebhookService(events repository.KYCEventRepository, investors repository.InvestorRepository, hmacSecret string) *WebhookService {
	return &WebhookService{events: events, investors: investors, secret: hmacSecret}
}

// Process verifies the HMAC signature of a generic-shape webhook, parses the
// payload and passes it to ProcessDecision. It returns the accepted decision,
// which the WebhookReconciler relays on-chain. A wallet that has not proven
// ownership is rejected unless allowManualOverride is set.
func (s *WebhookService) Process(ctx context.Context, payload []byte, signatureHex string, allowManualOverride bool) (WebhookPayload, error) {
	if !VerifyHMAC(payload, signatureHex, s.secret) {
		return WebhookPayload{}, ErrInvalidSignature
	}
	var wp WebhookPayload
	if err := json.Unmarshal(payload, &wp); err != nil {
		return WebhookPayload{}, fmt.Errorf("compliance: invalid webhook payload: %w", err)
	}
	return s.ProcessDecision(ctx, payload, wp, allowManualOverride)
}

// ProcessDecision handles an already-authenticated, already-parsed decision. It
// rejects a repeated payload, validates and normalizes wp, requires proven
// wallet ownership unless allowManualOverride is set, and durably records the
// event for the reconciler. The KYC webhook handler calls it after a provider
// adapter (internal/kyc) has verified the provider's own signature and mapped
// the payload to a WebhookPayload. rawForHash is the raw request body, hashed
// to detect resends.
func (s *WebhookService) ProcessDecision(ctx context.Context, rawForHash []byte, wp WebhookPayload, allowManualOverride bool) (WebhookPayload, error) {
	sum := sha256.Sum256(rawForHash)
	payloadHash := hex.EncodeToString(sum[:])
	// Fast path only. Two concurrent deliveries of one payload can both pass
	// this check, and the atomic check is the unique payloadHash constraint in
	// Create below, which also maps to ErrReplayed
	if exists, err := s.events.Exists(ctx, payloadHash); err != nil {
		return WebhookPayload{}, err
	} else if exists {
		return WebhookPayload{}, ErrReplayed
	}

	if wp.Status != "Allowed" && wp.Status != "Blocked" && wp.Status != "Pending" {
		return WebhookPayload{}, fmt.Errorf("compliance: unsupported status %q", wp.Status)
	}
	// provider and eventId together identify the delivery, so neither may be empty
	if wp.Provider == "" {
		return WebhookPayload{}, errors.New("compliance: webhook payload missing required provider")
	}
	if wp.EventID == "" {
		return WebhookPayload{}, errors.New("compliance: webhook payload missing required eventId")
	}
	if !common.IsHexAddress(wp.Address) {
		return WebhookPayload{}, errors.New("compliance: webhook payload address is not a valid hex address")
	}
	wp.Address = common.HexToAddress(wp.Address).Hex() // checksummed before any lookup, storage or relay

	if wp.OccurredAt <= 0 {
		return WebhookPayload{}, errors.New("compliance: webhook payload missing required occurredAt")
	}
	occurredAt := time.Unix(wp.OccurredAt, 0).UTC()
	now := time.Now().UTC()
	if occurredAt.After(now.Add(MaxWebhookClockSkewAhead)) {
		return WebhookPayload{}, fmt.Errorf("%w: occurredAt %s is implausibly far in the future", ErrStale, occurredAt)
	}
	if now.Sub(occurredAt) > MaxWebhookAge {
		return WebhookPayload{}, fmt.Errorf("%w: occurredAt %s is older than the %s delivery window", ErrStale, occurredAt, MaxWebhookAge)
	}

	// Reject a negative validUntil before the later int64 to uint64 conversion
	// for the on-chain call, and cap how far ahead it may be
	if wp.ValidUntil < 0 {
		return WebhookPayload{}, errors.New("compliance: webhook validUntil must not be negative")
	}
	if wp.ValidUntil > 0 {
		if vu := time.Unix(wp.ValidUntil, 0).UTC(); vu.After(now.Add(MaxValidUntilHorizon)) {
			return WebhookPayload{}, fmt.Errorf("compliance: webhook validUntil %s exceeds the %s policy horizon", vu, MaxValidUntilHorizon)
		}
	}

	if !allowManualOverride {
		inv, err := s.investors.Get(ctx, wp.Address)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return WebhookPayload{}, ErrOwnershipNotVerified
			}
			return WebhookPayload{}, err
		}
		if !inv.OwnershipVerified {
			return WebhookPayload{}, ErrOwnershipNotVerified
		}
	}

	eventKey := kycEventIDKey(wp.Provider, wp.EventID)

	// Record the delivery in the kyc_events history as Claiming before
	// advancing the per-address claim. If the claim advanced first and Create
	// then failed, the claim would point at a missing event, a retry could not
	// win the claim again, and a newest Blocked decision could be lost. With
	// this order a failure between the two steps leaves a Claiming event that
	// the reconciler resolves. Create enforces the unique payloadHash and
	// (provider, eventId) constraints, so a resend is rejected as a replay.
	event := &models.KYCEvent{
		ID: uuid.NewString(), Address: wp.Address, Status: wp.Status, Provider: wp.Provider, EventID: wp.EventID,
		Outcome: wp.Status, ApplyStatus: models.KYCApplyClaiming, ValidUntil: wp.ValidUntil, OccurredAt: occurredAt,
		ReceivedAt: now, PayloadHash: payloadHash,
	}
	if err := s.events.Create(ctx, event); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			// A resend of a stored decision. If the earlier attempt died before
			// finalizing, the event stays Claiming and the reconciler resolves it
			// regardless of this retry, so reporting a replay loses nothing
			return WebhookPayload{}, ErrReplayed
		}
		return WebhookPayload{}, err
	}

	// Advance the per-address claim to the stored event and set its outbox
	// state: Accepted or Recorded if it holds the claim, otherwise Superseded
	// by the (occurredAt, eventKey) ordering. A failure here leaves the event
	// Claiming, and the reconciler finalizes it on a later tick.
	holds, err := finalizeClaim(ctx, s.events, event)
	if err != nil {
		return WebhookPayload{}, err
	}
	if !holds {
		// A newer or equal decision already holds the claim. This delivery is
		// stored as Superseded and is not applied on-chain. The same
		// (provider, eventId) holding the claim is a replay, anything else is
		// stale.
		if currentKey, err := s.events.CurrentClaimEventKey(ctx, wp.Address); err == nil && currentKey == eventKey {
			return WebhookPayload{}, ErrReplayed
		}
		return WebhookPayload{}, ErrStale
	}

	return wp, nil
}

// finalizeClaim resolves a stored Claiming event. It runs the per-address claim
// compare-and-set and sets the event to Accepted (Allowed or Blocked, queued
// for the reconciler), Recorded (Pending, no on-chain effect) or, if a newer
// decision holds the claim, Superseded. It is idempotent, so both
// ProcessDecision and the reconciler can call it: an event that already holds
// the claim is not strictly newer than itself, but CurrentClaimEventKey shows
// it holds the claim, so its state is unchanged. It returns whether the event
// holds the claim.
func finalizeClaim(ctx context.Context, events repository.KYCEventRepository, e *models.KYCEvent) (bool, error) {
	eventKey := kycEventIDKey(e.Provider, e.EventID)
	won, err := events.ClaimLatestForAddress(ctx, e.Address, e.OccurredAt, eventKey)
	if err != nil {
		return false, err
	}
	holds := won
	if !holds {
		currentKey, err := events.CurrentClaimEventKey(ctx, e.Address)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return false, err
		}
		holds = err == nil && currentKey == eventKey
	}
	if holds {
		if e.Outcome == "Pending" {
			e.ApplyStatus = models.KYCApplyRecorded
		} else {
			e.ApplyStatus = models.KYCApplyAccepted
		}
	} else {
		e.ApplyStatus = models.KYCApplySuperseded
	}
	if err := events.Update(ctx, e); err != nil {
		return holds, err
	}
	return holds, nil
}

// kycEventIDKey is the eventKey that ClaimLatestForAddress compares after
// occurredAt. It must equal the string the memory and mongodb repositories
// derive on their own, since neither imports this package.
func kycEventIDKey(provider, eventID string) string { return provider + "|" + eventID }
