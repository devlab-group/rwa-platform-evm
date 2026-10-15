package models

import "time"

// KYCApplyStatus tracks a stored decision through claiming, relay or a terminal
// outcome. Authentication precedes storage; ownership is required unless manually
// overridden. Create enforces replay uniqueness, and chain submission follows
// acceptance.
type KYCApplyStatus string

const (
	// KYCApplyClaiming is ProcessDecision's initial persisted state, recoverable after
	// interrupted claim finalization. ListPending includes it; finalizeClaim selects
	// Accepted/Recorded for claim holders or Superseded for losers, preserving decisions
	// such as Blocked through crashes.
	KYCApplyClaiming KYCApplyStatus = "Claiming"
	// KYCApplyAccepted waits for reconciler submission. TxFailed or TxReorged, including a
	// post-confirmation reorg, can return an event here.
	KYCApplyAccepted KYCApplyStatus = "Accepted"
	// KYCApplyApplying means a transaction was submitted (TxID is set) and awaits
	// confirmation
	KYCApplyApplying KYCApplyStatus = "Applying"
	// KYCApplyApplied means the linked transaction confirmed. A linked TxReorged reopens
	// the event as Accepted.
	KYCApplyApplied KYCApplyStatus = "Applied"
	// KYCApplySuperseded abandons an event displaced by a newer claim before completion.
	// It is terminal, not a failure.
	KYCApplySuperseded KYCApplyStatus = "Superseded"
	// KYCApplyFailed is terminal and needs operator attention, following an on-chain
	// revert or malformed relay outcome
	KYCApplyFailed KYCApplyStatus = "Failed"
	// KYCApplyRecorded retains a Pending provider outcome in history without an on-chain
	// ComplianceStatus change. It is terminal.
	KYCApplyRecorded KYCApplyStatus = "Recorded"
)

// KYCEvent is a durably queued webhook decision (collection: kyc_events)
type KYCEvent struct {
	ID       string `json:"id" bson:"_id"`
	Address  string `json:"address" bson:"address"`
	Status   string `json:"status" bson:"status"` // deprecated alias of Outcome, kept for backward compatibility
	Provider string `json:"provider" bson:"provider"`
	// EventID and Provider identify a delivery for uniqueness checks. The webhook workflow
	// requires both; MongoDB's partial index excludes empty eventId values.
	EventID string `json:"eventId" bson:"eventId"`
	Outcome string `json:"outcome" bson:"outcome"` // "Allowed" | "Blocked" | "Pending"
	// ApplyStatus is the event's outbox state, see KYCApplyStatus
	ApplyStatus KYCApplyStatus `json:"applyStatus" bson:"applyStatus"`
	// ValidUntil retains the requested expiry for later relay without the request body; 0
	// means no expiry
	ValidUntil int64 `json:"validUntil,omitempty" bson:"validUntil,omitempty"`
	// TxID links a submitted transaction; retry or reorg recovery may clear it
	TxID string `json:"txId,omitempty" bson:"txId,omitempty"`
	// OccurredAt is signed provider decision time, driving freshness and per-address
	// ordering. ReceivedAt is server receipt time.
	OccurredAt  time.Time `json:"occurredAt" bson:"occurredAt"`
	ReceivedAt  time.Time `json:"receivedAt" bson:"receivedAt"`
	PayloadHash string    `json:"payloadHash" bson:"payloadHash"`
}
