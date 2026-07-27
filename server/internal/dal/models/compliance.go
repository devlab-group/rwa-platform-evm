package models

import "time"

// ComplianceStatus mirrors IComplianceRegistry.ComplianceStatus.
type ComplianceStatus string

const (
	ComplianceUnknown ComplianceStatus = "Unknown"
	ComplianceAllowed ComplianceStatus = "Allowed"
	ComplianceBlocked ComplianceStatus = "Blocked"
)

// Investor is a wallet's known status (collection: investors).
type Investor struct {
	Address           string           `json:"address" bson:"_id"`
	Status            ComplianceStatus `json:"status" bson:"status"`
	ValidUntil        int64            `json:"validUntil" bson:"validUntil"`
	OwnershipVerified bool             `json:"ownershipVerified" bson:"ownershipVerified"`
	CreatedAt         time.Time        `json:"createdAt" bson:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt" bson:"updatedAt"`
}

// WalletChallenge is a one-time wallet-ownership proof challenge
// (collection: wallet_challenges).
type WalletChallenge struct {
	ID        string    `json:"id" bson:"_id"`
	Address   string    `json:"address" bson:"address"`
	Nonce     string    `json:"nonce" bson:"nonce"`
	Message   string    `json:"message" bson:"message"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	Used      bool      `json:"used" bson:"used"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

// KYCEvent is a raw processed webhook delivery, kept for replay detection
// and the compliance webhook-history screen (collection: kyc_events).
// KYCApplyStatus is the outbox half of the webhook inbox/outbox state
// machine, tracking a webhook decision from the moment it is consumed until
// its on-chain status is durably applied. Replaces the old bare `Applied
// bool`, which was set true the instant HMAC/replay/ownership verification
// passed — BEFORE the compliance transaction was even submitted, so a
// failed relay left a permanently "applied" record with no matching
// on-chain effect and no way for the provider to retry (payloadHash/
// (provider,eventId) uniqueness rejects every redelivery as a replay).
type KYCApplyStatus string

const (
	// KYCApplyClaiming: durably recorded in the append-only kyc_events
	// history, but the per-address "latest decision" claim has not yet been
	// resolved. This is the FIRST state WebhookService.Process
	// writes: the event is made durable BEFORE the claim advances, so a
	// crash or transient persistence failure at the claim/finalize boundary
	// can never lose a valid decision (especially a Blocked one). The
	// reconciler re-resolves every Claiming event (finalizeClaim) into
	// Accepted/Recorded (it holds the claim) or Superseded (a newer decision
	// won), so it is included in ListPending. Not terminal.
	KYCApplyClaiming KYCApplyStatus = "Claiming"
	// KYCApplyAccepted: durably stored, won its (occurredAt,eventKey)
	// claim, but no on-chain submission has been attempted yet (or a
	// prior attempt definitively failed to broadcast and is retryable —
	// see TxFailed's doc comment). The reconciler's queue.
	KYCApplyAccepted KYCApplyStatus = "Accepted"
	// KYCApplyApplying: a transaction intent has been submitted (TxID set)
	// and is awaiting confirmation.
	KYCApplyApplying KYCApplyStatus = "Applying"
	// KYCApplyApplied: the linked transaction confirmed on-chain. Terminal.
	KYCApplyApplied KYCApplyStatus = "Applied"
	// KYCApplySuperseded: a newer decision for the same address won the
	// claim before this one was applied — correctly abandoned, not a
	// failure. Terminal.
	KYCApplySuperseded KYCApplyStatus = "Superseded"
	// KYCApplyFailed: the linked transaction reverted on-chain (a genuine
	// on-chain rejection, not a broadcast ambiguity TxManager already
	// retries) — needs operator attention. Terminal.
	KYCApplyFailed KYCApplyStatus = "Failed"
	// KYCApplyRecorded: a "Pending" KYC outcome (still under provider
	// review) has no on-chain ComplianceStatus counterpart to apply at
	// all — recorded for the webhook-history screen only. Terminal.
	KYCApplyRecorded KYCApplyStatus = "Recorded"
)

// KYCEvent is a durably queued webhook decision (collection: kyc_events).
type KYCEvent struct {
	ID       string `json:"id" bson:"_id"`
	Address  string `json:"address" bson:"address"`
	Status   string `json:"status" bson:"status"` // deprecated alias of Outcome, kept for backward compatibility
	Provider string `json:"provider" bson:"provider"`
	// EventID is the provider's own delivery identifier — together with
	// Provider, the logical uniqueness key Create enforces. Both may be
	// empty, in which case they don't act as uniqueness keys.
	EventID string `json:"eventId" bson:"eventId"`
	Outcome string `json:"outcome" bson:"outcome"` // "Allowed" | "Blocked" | "Pending"
	// ApplyStatus is this event's outbox state — see KYCApplyStatus.
	ApplyStatus KYCApplyStatus `json:"applyStatus" bson:"applyStatus"`
	// ValidUntil is the requested on-chain expiry (0 = no expiry),
	// preserved from the payload so the reconciler can submit the
	// compliance transaction without needing the original HTTP request.
	ValidUntil int64 `json:"validUntil,omitempty" bson:"validUntil,omitempty"`
	// TxID links to the Transaction record once a submission is in flight
	// (ApplyStatus Applying or later).
	TxID string `json:"txId,omitempty" bson:"txId,omitempty"`
	// OccurredAt is when the provider made this decision (a signed occurrence
	// timestamp from the payload), NOT when this server received the
	// delivery — see ReceivedAt for that. Used for the bounded-delivery-
	// window freshness check and for rejecting a stale/out-of-order
	// decision for the same address.
	OccurredAt  time.Time `json:"occurredAt" bson:"occurredAt"`
	ReceivedAt  time.Time `json:"receivedAt" bson:"receivedAt"`
	PayloadHash string    `json:"payloadHash" bson:"payloadHash"`
}

// KYCVerification binds a KYC provider's own subject reference back to the
// wallet address it was started for (collection: kyc_verifications). It exists
// because providers differ in how a webhook identifies its subject: Sumsub
// echoes the externalUserId we set (the wallet address itself), so it needs no
// lookup — but Onfido's webhook carries only an applicant / workflow-run id and
// no custom field, so the address is resolved through this record, written when
// POST /compliance/kyc/start creates the provider-side verification. ID is
// KYCVerificationID(Provider, Ref); Ref is the provider reference the webhook
// carries (Onfido workflow-run id; for Sumsub, the externalUserId = address).
type KYCVerification struct {
	ID        string    `json:"id" bson:"_id"`
	Provider  string    `json:"provider" bson:"provider"`
	Ref       string    `json:"ref" bson:"ref"`
	Address   string    `json:"address" bson:"address"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

// KYCVerificationID is the (provider,ref) technical key used as the
// kyc_verifications _id. Kept as one helper so the repository adapters and the
// handler that writes/reads these records all derive the same key.
func KYCVerificationID(provider, ref string) string { return provider + "|" + ref }

// ComplianceOperation is a submitted status-change transaction
// (collection: compliance_operations).
type ComplianceOperation struct {
	ID         string           `json:"id" bson:"_id"`
	Address    string           `json:"address" bson:"address"`
	Status     ComplianceStatus `json:"status" bson:"status"`
	ValidUntil int64            `json:"validUntil" bson:"validUntil"`
	TxHash     string           `json:"txHash" bson:"txHash"`
	Caller     string           `json:"caller" bson:"caller"`
	CreatedAt  time.Time        `json:"createdAt" bson:"createdAt"`
}
