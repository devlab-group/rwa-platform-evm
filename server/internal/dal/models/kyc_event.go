package models

import "time"

// KYCApplyStatus is the outbox state of a webhook decision, from when it is stored
// until its on-chain status is applied. Verification of the signature, replay and
// wallet ownership happens before the event is stored, and the on-chain
// transaction is submitted afterwards, so the status says how far the decision has
// got.
type KYCApplyStatus string

const (
	// KYCApplyClaiming means the event is stored in the append-only kyc_events history
	// but the per-address latest-decision claim is not resolved yet. It is the first
	// state WebhookService.Process writes, so a crash before the claim resolves cannot
	// lose a valid decision, in particular a Blocked one. The reconciler resolves
	// Claiming events (finalizeClaim) to Accepted or Recorded if the event holds the
	// claim, or Superseded if a newer decision won. ListPending includes it. It is not
	// terminal.
	KYCApplyClaiming KYCApplyStatus = "Claiming"
	// KYCApplyAccepted means the event holds the claim and is waiting for an on-chain
	// submission, which the reconciler makes. An event also returns here when its
	// transaction failed or was reorged before it confirmed.
	KYCApplyAccepted KYCApplyStatus = "Accepted"
	// KYCApplyApplying means a transaction was submitted (TxID is set) and awaits
	// confirmation
	KYCApplyApplying KYCApplyStatus = "Applying"
	// KYCApplyApplied means the linked transaction confirmed on-chain. A reorg of that
	// transaction returns the event to Accepted.
	KYCApplyApplied KYCApplyStatus = "Applied"
	// KYCApplySuperseded means a newer decision for the same address won the claim
	// before this one was applied. It is abandoned and is not a failure. Terminal.
	KYCApplySuperseded KYCApplyStatus = "Superseded"
	// KYCApplyFailed means the linked transaction was mined and reverted on-chain,
	// which needs operator attention. Terminal.
	KYCApplyFailed KYCApplyStatus = "Failed"
	// KYCApplyRecorded means the outcome is "Pending" (still under provider review),
	// which has no ComplianceStatus to apply on-chain. It is kept for the webhook
	// history only. Terminal.
	KYCApplyRecorded KYCApplyStatus = "Recorded"
)

// KYCEvent is a durably queued webhook decision (collection: kyc_events)
type KYCEvent struct {
	ID       string `json:"id" bson:"_id"`
	Address  string `json:"address" bson:"address"`
	Status   string `json:"status" bson:"status"` // deprecated alias of Outcome, kept for backward compatibility
	Provider string `json:"provider" bson:"provider"`
	// EventID is the provider's delivery identifier. With Provider it forms the
	// logical uniqueness key that Create enforces. If either is empty, the pair is not
	// enforced as unique.
	EventID string `json:"eventId" bson:"eventId"`
	Outcome string `json:"outcome" bson:"outcome"` // "Allowed" | "Blocked" | "Pending"
	// ApplyStatus is the event's outbox state, see KYCApplyStatus
	ApplyStatus KYCApplyStatus `json:"applyStatus" bson:"applyStatus"`
	// ValidUntil is the requested on-chain expiry, 0 for none, kept from the payload
	// so the reconciler can submit the transaction without the original request
	ValidUntil int64 `json:"validUntil,omitempty" bson:"validUntil,omitempty"`
	// TxID is the id of the Transaction record once a submission is in flight
	// (ApplyStatus Applying or later)
	TxID string `json:"txId,omitempty" bson:"txId,omitempty"`
	// OccurredAt is when the provider made the decision, taken from a signed
	// timestamp in the payload. ReceivedAt is when this server received it.
	// OccurredAt drives the delivery-window freshness check and the rejection of a
	// stale or out-of-order decision for the same address.
	OccurredAt  time.Time `json:"occurredAt" bson:"occurredAt"`
	ReceivedAt  time.Time `json:"receivedAt" bson:"receivedAt"`
	PayloadHash string    `json:"payloadHash" bson:"payloadHash"`
}
