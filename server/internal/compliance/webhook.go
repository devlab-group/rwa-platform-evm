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

// ErrReplayed rejects duplicate payload hashes or (provider, eventId) pairs, including
// reserialized deliveries
var ErrReplayed = errors.New("compliance: webhook payload already processed (replay)")

// ErrStale rejects occurredAt outside the delivery window or a decision ordered behind
// the address's claim holder
var ErrStale = errors.New("compliance: webhook decision is stale (outside the delivery window, or older than an already-applied decision)")

// ErrOwnershipNotVerified rejects wallets without ownership proof unless
// allowManualOverride is set
var ErrOwnershipNotVerified = errors.New("compliance: wallet has not proven ownership; requires a documented manual override")

const (
	// MaxWebhookAge limits delayed replay of signed decisions by bounding occurredAt age
	MaxWebhookAge = 24 * time.Hour
	// MaxWebhookClockSkewAhead tolerates provider/server clock drift by bounding future
	// occurredAt
	MaxWebhookClockSkewAhead = 5 * time.Minute
	// MaxValidUntilHorizon is how far in the future a validUntil may be
	MaxValidUntilHorizon = 100 * 365 * 24 * time.Hour // ~100 years
)

// WebhookPayload requires Provider and EventID for delivery identity, and OccurredAt
// for freshness and ordering, across KYC providers
type WebhookPayload struct {
	EventID  string `json:"eventId"`
	Address  string `json:"address"`
	Provider string `json:"provider"`
	Status   string `json:"status"` // "Allowed", "Blocked" or "Pending"
	// OccurredAt is provider decision time in Unix seconds, covered by the generic
	// payload's HMAC
	OccurredAt int64 `json:"occurredAt"`
	ValidUntil int64 `json:"validUntil,omitempty"`
}

// VerifyHMAC checks hex signatureHex, optionally 0x-prefixed, as HMAC-SHA256 over
// payload using constant-time comparison. Empty secrets always fail because anyone
// could forge their signatures. Config loaders also enforce required-secret length;
// this guard protects direct callers.
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

// Process verifies generic webhook HMAC, decodes JSON and calls ProcessDecision.
// Ownership proof is required unless allowManualOverride is set. Only Accepted
// Allowed/Blocked decisions enter on-chain relay; Pending is recorded without relay.
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

// ProcessDecision validates and normalizes an authenticated, parsed decision, checks
// replay and wallet ownership unless allowManualOverride is set, and stores it for
// reconciliation. The API calls it after internal/kyc verifies and maps provider
// payloads. rawForHash is the original body used for replay hashing.
func (s *WebhookService) ProcessDecision(ctx context.Context, rawForHash []byte, wp WebhookPayload, allowManualOverride bool) (WebhookPayload, error) {
	sum := sha256.Sum256(rawForHash)
	payloadHash := hex.EncodeToString(sum[:])
	// Exists is a fast pre-check, not concurrency protection. Create's unique payloadHash
	// constraint rejects racing duplicates as ErrReplayed.
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

	// Reject negative validUntil before unsigned conversion and cap its future horizon
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

	// Persist Claiming before advancing the address claim, so a failed Create cannot
	// strand a claim or lose the newest Blocked decision. Interrupted finalization remains
	// recoverable by the reconciler. Create's unique payloadHash and (provider, eventId)
	// constraints reject resends.
	event := &models.KYCEvent{
		ID: uuid.NewString(), Address: wp.Address, Status: wp.Status, Provider: wp.Provider, EventID: wp.EventID,
		Outcome: wp.Status, ApplyStatus: models.KYCApplyClaiming, ValidUntil: wp.ValidUntil, OccurredAt: occurredAt,
		ReceivedAt: now, PayloadHash: payloadHash,
	}
	if err := s.events.Create(ctx, event); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			// The delivery is already stored. Any unfinished Claiming event remains recoverable
			// independently of this replay.
			return WebhookPayload{}, ErrReplayed
		}
		return WebhookPayload{}, err
	}

	// Finalize by (occurredAt, eventKey): the claim holder becomes Accepted or Recorded;
	// losers become Superseded. Failure leaves Claiming for reconciliation.
	holds, err := finalizeClaim(ctx, s.events, event)
	if err != nil {
		return WebhookPayload{}, err
	}
	if !holds {
		// Store the losing delivery as Superseded without relay. A matching current event key
		// reports replay; any other loss reports stale.
		if currentKey, err := s.events.CurrentClaimEventKey(ctx, wp.Address); err == nil && currentKey == eventKey {
			return WebhookPayload{}, ErrReplayed
		}
		return WebhookPayload{}, ErrStale
	}

	return wp, nil
}

// finalizeClaim resolves stored Claiming events with a per-address compare-and-set.
// Holders become Accepted for Allowed/Blocked or Recorded for Pending; losers become
// Superseded. CurrentClaimEventKey recognizes a prior successful claim after
// interrupted finalization, allowing ProcessDecision or the reconciler to repeat
// recovery. It returns whether this event holds the claim.
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

// kycEventIDKey breaks occurredAt ties in ClaimLatestForAddress. Its encoding must
// match memory and MongoDB repositories, which do not import compliance.
func kycEventIDKey(provider, eventID string) string { return provider + "|" + eventID }
