package dto

import (
	"strings"

	"github.com/rwa-platform/server/internal/dal/models"
)

// WalletStatus mirrors components.schemas.WalletStatus
type WalletStatus struct {
	Address           string `json:"address"`
	Status            string `json:"status"`
	ValidUntil        int64  `json:"validUntil"`
	OwnershipVerified bool   `json:"ownershipVerified"`
	// FrozenTokens is this wallet's ERC-7943 frozen amount in token minimal
	// units (base-10 string), omitted when nothing is frozen. The map of all
	// frozen wallets is admin-only (see EnforcementResponse).
	FrozenTokens string `json:"frozenTokens,omitempty"`
}

// ToWalletStatus maps one stored investor record onto its API view
func ToWalletStatus(inv *models.Investor) WalletStatus {
	return WalletStatus{
		Address: inv.Address, Status: string(inv.Status),
		ValidUntil: inv.ValidUntil, OwnershipVerified: inv.OwnershipVerified,
	}
}

// WithFrozenTokens returns the status with this wallet's frozen amount filled
// in from the security projection. The lookup is case-insensitive because the
// projection stores checksummed addresses. Callers pass only the session
// wallet's own status.
func (w WalletStatus) WithFrozenTokens(p *models.Project) WalletStatus {
	if p == nil || p.Security == nil {
		return w
	}
	for addr, amount := range p.Security.FrozenBalances {
		if strings.EqualFold(addr, w.Address) {
			w.FrozenTokens = amount
			return w
		}
	}
	return w
}

// AllowedResult mirrors components.schemas.AllowedResult. It holds one boolean
// because the endpoint is public and must not disclose a third party's full
// WalletStatus (see isAddressAllowed in package api).
type AllowedResult struct {
	Allowed bool `json:"allowed"`
}

// VerifyChallengeResult mirrors components.schemas.VerifyChallengeResult: the
// verified address's WalletStatus plus the wallet session issued after a
// successful ownership proof. SessionToken and SessionExpiresAt are omitted when
// no SessionManager is configured.
type VerifyChallengeResult struct {
	Address           string `json:"address"`
	Status            string `json:"status"`
	ValidUntil        int64  `json:"validUntil"`
	OwnershipVerified bool   `json:"ownershipVerified"`
	SessionToken      string `json:"sessionToken,omitempty"`
	SessionExpiresAt  string `json:"sessionExpiresAt,omitempty"`
}

// TxRef mirrors components.schemas.TxRef
type TxRef struct {
	TxHash         string `json:"txHash"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// ToTxRef maps a submitted transaction record onto the minimal reference an
// accepted (202) side-effecting response returns
func ToTxRef(tx *models.Transaction) TxRef {
	return TxRef{TxHash: tx.TxHash, Status: string(tx.Status), IdempotencyKey: tx.IdempotencyKey}
}

// KYCSession mirrors components.schemas.KYCSession: what POST
// /compliance/kyc/start returns so the investor app can launch the provider's
// verification flow. Depending on the provider, Token is an SDK token or URL is
// a hosted link. Ref is the provider-side reference (for Onfido, the workflow
// run id)
type KYCSession struct {
	Provider  string `json:"provider"`
	Token     string `json:"token,omitempty"`
	URL       string `json:"url,omitempty"`
	Ref       string `json:"ref,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// ChallengeResponse mirrors components.schemas.Challenge
type ChallengeResponse struct {
	Address   string `json:"address"`
	Nonce     string `json:"nonce"`
	Message   string `json:"message"`
	ExpiresAt string `json:"expiresAt"`
}

// ToChallengeResponse maps a freshly issued wallet challenge onto its API view
func ToChallengeResponse(ch *models.WalletChallenge) ChallengeResponse {
	return ChallengeResponse{
		Address: ch.Address, Nonce: ch.Nonce, Message: ch.Message,
		ExpiresAt: ch.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// WebhookEventResponse mirrors components.schemas.WebhookEvent. EventID and
// OccurredAt are omitted for records that lack them.
//
// ApplyStatus is the outbox state (see models.KYCApplyStatus). Applied is true
// only when ApplyStatus is "Applied", that is, the linked on-chain transaction
// confirmed.
type WebhookEventResponse struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	Provider    string `json:"provider"`
	EventID     string `json:"eventId,omitempty"`
	Outcome     string `json:"outcome"`
	OccurredAt  string `json:"occurredAt,omitempty"`
	ReceivedAt  string `json:"receivedAt"`
	ApplyStatus string `json:"applyStatus"`
	Applied     bool   `json:"applied"`
}

// ToWebhookEventResponse maps one stored KYC webhook delivery onto its API
// view. OccurredAt stays empty for a record that never carried a provider
// occurrence timestamp.
func ToWebhookEventResponse(e *models.KYCEvent) WebhookEventResponse {
	out := WebhookEventResponse{
		ID: e.ID, Address: e.Address, Provider: e.Provider, EventID: e.EventID, Outcome: e.Outcome,
		ReceivedAt:  e.ReceivedAt.Format("2006-01-02T15:04:05Z07:00"),
		ApplyStatus: string(e.ApplyStatus), Applied: e.ApplyStatus == models.KYCApplyApplied,
	}
	if !e.OccurredAt.IsZero() {
		out.OccurredAt = e.OccurredAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

// AuditLogResponse mirrors components.schemas.AuditLog
type AuditLogResponse struct {
	ID        string         `json:"id"`
	Category  string         `json:"category"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	CreatedAt string         `json:"createdAt"`
	Details   map[string]any `json:"details,omitempty"`
}

// ToAuditLogResponse maps one operational audit entry onto its API view
func ToAuditLogResponse(e *models.AuditLogEntry) AuditLogResponse {
	return AuditLogResponse{
		ID: e.ID, Category: e.Category, Actor: e.Actor, Action: e.Action, Target: e.Target,
		CreatedAt: e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), Details: e.Metadata,
	}
}
