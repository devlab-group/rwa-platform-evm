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

// ErrInvalidSignature is returned when the webhook HMAC does not verify.
var ErrInvalidSignature = errors.New("compliance: invalid webhook signature")

// ErrReplayed is returned for a webhook payload already processed once —
// either a byte-for-byte resend (same payload hash) or the same logical
// event reserialized/re-signed under the same (provider,eventId).
var ErrReplayed = errors.New("compliance: webhook payload already processed (replay)")

// ErrStale is returned when a webhook's OccurredAt is outside the bounded
// delivery window, or is not strictly newer than the latest already-applied
// decision for the same address. Deliveries must fall inside a bounded
// window, and newer/older decisions have a defined ordering.
var ErrStale = errors.New("compliance: webhook decision is stale (outside the delivery window, or older than an already-applied decision)")

// ErrOwnershipNotVerified is returned when the webhook targets a wallet
// that has not completed the ownership challenge and the caller did not
// pass allowManualOverride.
var ErrOwnershipNotVerified = errors.New("compliance: wallet has not proven ownership; requires a documented manual override")

const (
	// MaxWebhookAge bounds how old a delivery's occurredAt may be — the
	// bounded delivery window. Without it, a correctly signed decision that
	// was captured but never delivered remains valid indefinitely.
	MaxWebhookAge = 24 * time.Hour
	// MaxWebhookClockSkewAhead tolerates modest clock drift between this
	// server and the provider without accepting an implausibly
	// far-future occurredAt.
	MaxWebhookClockSkewAhead = 5 * time.Minute
	// MaxValidUntilHorizon bounds how far in the future a compliance
	// validUntil may be — an out-of-policy-expiration ceiling, independent
	// of rejecting a negative value outright.
	MaxValidUntilHorizon = 100 * 365 * 24 * time.Hour // ~100 years
)

// WebhookPayload is the generic (provider-agnostic) KYC decision shape the
// platform expects. Provider-specific KYC adapters are out of scope; this is
// the minimal contract the platform commits to. Provider, EventID, and
// OccurredAt are all required: together they're the replay/freshness identity
// of a delivery, not optional metadata.
type WebhookPayload struct {
	EventID  string `json:"eventId"`
	Address  string `json:"address"`
	Provider string `json:"provider"`
	Status   string `json:"status"` // "Allowed" | "Blocked" | "Pending" (api Schemas.WebhookEvent.outcome)
	// OccurredAt is a Unix-seconds timestamp for when the provider made
	// this decision, covered by the same HMAC signature as the rest of
	// the payload — a signed occurrence timestamp.
	OccurredAt int64 `json:"occurredAt"`
	ValidUntil int64 `json:"validUntil,omitempty"`
}

// VerifyHMAC reports whether signatureHex (hex-encoded, optionally
// "0x"-prefixed) is a valid HMAC-SHA256 of payload under secret. It uses a
// constant-time comparison to avoid timing side channels.
//
// An empty secret ALWAYS fails, independent of signatureHex: HMAC-SHA256
// with an empty key is a well-defined, publicly computable function, so
// treating it as "valid" would let anyone forge a signature for an
// unconfigured webhook. config.Load's production checks are
// the primary defense (see MinWebhookSecretBytes); this is the
// defense-in-depth backstop for every other environment/call site.
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

// WebhookService validates and records inbound KYC webhook deliveries.
type WebhookService struct {
	events    repository.KYCEventRepository
	investors repository.InvestorRepository
	secret    string
}

// NewWebhookService constructs a WebhookService.
func NewWebhookService(events repository.KYCEventRepository, investors repository.InvestorRepository, hmacSecret string) *WebhookService {
	return &WebhookService{events: events, investors: investors, secret: hmacSecret}
}

// Process verifies the HMAC signature, rejects a replayed payload, and
// requires the target wallet to have already proven ownership unless
// allowManualOverride is set: the server must not accept a wallet address
// from a KYC webhook unless the wallet has previously proven ownership, or
// the operator explicitly performs a documented manual override. It returns
// the parsed decision for the caller to relay on-chain via SubmitStatus.
func (s *WebhookService) Process(ctx context.Context, payload []byte, signatureHex string, allowManualOverride bool) (WebhookPayload, error) {
	if !VerifyHMAC(payload, signatureHex, s.secret) {
		return WebhookPayload{}, ErrInvalidSignature
	}

	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	// Fast-path only: under a genuine race between two concurrent
	// deliveries of the same payload, both can pass this check before
	// either has Create()d — that's fine, since it's just an optimization
	// to skip ownership-verification work for an ALREADY-known replay; the
	// real, atomic authority is Create's unique-payloadHash rejection
	// below, whose error this function maps to ErrReplayed either way.
	if exists, err := s.events.Exists(ctx, payloadHash); err != nil {
		return WebhookPayload{}, err
	} else if exists {
		return WebhookPayload{}, ErrReplayed
	}

	var wp WebhookPayload
	if err := json.Unmarshal(payload, &wp); err != nil {
		return WebhookPayload{}, fmt.Errorf("compliance: invalid webhook payload: %w", err)
	}
	if wp.Status != "Allowed" && wp.Status != "Blocked" && wp.Status != "Pending" {
		return WebhookPayload{}, fmt.Errorf("compliance: unsupported status %q", wp.Status)
	}
	// provider and eventId are required identity fields, not
	// optional metadata — an empty value can never satisfy the
	// (provider,eventId) uniqueness constraint meaningfully.
	if wp.Provider == "" {
		return WebhookPayload{}, errors.New("compliance: webhook payload missing required provider")
	}
	if wp.EventID == "" {
		return WebhookPayload{}, errors.New("compliance: webhook payload missing required eventId")
	}
	if !common.IsHexAddress(wp.Address) {
		return WebhookPayload{}, errors.New("compliance: webhook payload address is not a valid hex address")
	}
	wp.Address = common.HexToAddress(wp.Address).Hex() // checksummed/normalized before any lookup, storage, or relay

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

	// Reject a negative validUntil BEFORE any int64->uint64
	// conversion happens (the caller casts this for the on-chain call),
	// and cap how far in the future one may be.
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

	// Durably record the delivery in the append-only kyc_events
	// history FIRST — as Claiming — BEFORE advancing the per-address claim.
	// The reverse order (ClaimLatestForAddress, then Create) is not atomic:
	// if Create failed after the claim advanced, the claim pointed at an
	// event that did not exist, the same (provider,eventId) could no longer
	// win the "strictly newer" claim on retry (returned ErrReplayed), and any
	// older Accepted event was superseded — so a legitimate newest Blocked
	// decision could disappear from the durable outbox and never reach the
	// chain. Ordering the durable write first (and having the reconciler
	// re-resolve any orphaned Claiming event) guarantees the decision is
	// never lost by a transient failure at the claim/finalize boundary.
	// Create still enforces the payloadHash and (provider,eventId) uniqueness
	// constraints atomically, so a byte-identical or reserialized resend is
	// rejected as a replay rather than double-recorded.
	event := &models.KYCEvent{
		ID: uuid.NewString(), Address: wp.Address, Status: wp.Status, Provider: wp.Provider, EventID: wp.EventID,
		Outcome: wp.Status, ApplyStatus: models.KYCApplyClaiming, ValidUntil: wp.ValidUntil, OccurredAt: occurredAt,
		ReceivedAt: now, PayloadHash: payloadHash,
	}
	if err := s.events.Create(ctx, event); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			// A resend of an already-durable decision (same payload hash, or
			// the same logical (provider,eventId) reserialized as different
			// bytes). If a prior attempt died before its claim/finalize
			// completed, the stored event is left Claiming and the reconciler
			// repairs it independently of this HTTP retry — so reporting
			// replay here never loses the decision.
			return WebhookPayload{}, ErrReplayed
		}
		return WebhookPayload{}, err
	}

	// Advance the per-address claim to this now-durable event and resolve its
	// outbox state: Accepted/Recorded if it holds the claim, else Superseded
	// by the deterministic (occurredAt,eventKey) ordering. A transient
	// failure here leaves the event Claiming and durable — the reconciler
	// finalizes it on a later tick — so the decision survives even though this
	// call returns an error.
	holds, err := finalizeClaim(ctx, s.events, event)
	if err != nil {
		return WebhookPayload{}, err
	}
	if !holds {
		// A newer-or-equal decision for this address already holds the claim.
		// This delivery is durably recorded as Superseded history but must
		// not be applied on-chain. Distinguish an exact (provider,eventId)
		// re-claim (replay) from a genuinely older/lower decision (stale).
		if currentKey, err := s.events.CurrentClaimEventKey(ctx, wp.Address); err == nil && currentKey == eventKey {
			return WebhookPayload{}, ErrReplayed
		}
		return WebhookPayload{}, ErrStale
	}

	return wp, nil
}

// finalizeClaim resolves a durably-created (Claiming) event by running the
// atomic per-address claim compare-and-set and promoting the event to its
// resolved outbox state: it holds the claim -> Accepted (Allowed/Blocked,
// queued for the reconciler) or Recorded (Pending, no on-chain effect); a
// newer decision holds the claim -> Superseded. It is idempotent and safe to
// call from both WebhookService.Process and the reconciler's repair path:
// re-finalizing an event that already holds the claim
// re-observes holds=true via CurrentClaimEventKey (its own (occurredAt,
// eventKey) is not strictly newer than itself, but it IS the current claim)
// and leaves the resolved state intact. Returns whether the event holds the
// claim after the call.
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

// kycEventIDKey builds the deterministic identity key ClaimLatestForAddress
// uses as the (occurredAt,eventKey) tie-break's secondary component. MUST
// match every KYCEventRepository implementation's own derivation exactly
// (memory and mongodb each build the identical string independently, since
// neither depends on this package).
func kycEventIDKey(provider, eventID string) string { return provider + "|" + eventID }
