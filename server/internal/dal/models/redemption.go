package models

import "time"

// RedemptionStatus mirrors IRedemptionEscrow.RedemptionStatus
type RedemptionStatus string

const (
	RedemptionNone      RedemptionStatus = "None"
	RedemptionPending   RedemptionStatus = "Pending"
	RedemptionFunded    RedemptionStatus = "Funded"
	RedemptionCompleted RedemptionStatus = "Completed"
	RedemptionRejected  RedemptionStatus = "Rejected"
	RedemptionCancelled RedemptionStatus = "Cancelled"
)

// RedemptionRequest is reconstructed from chain events in redemption_requests. Server
// annotations must preserve event-derived status.
type RedemptionRequest struct {
	ID            string           `json:"id" bson:"_id"`
	Beneficiary   string           `json:"beneficiary" bson:"beneficiary"`
	RWAAmount     string           `json:"rwaAmount" bson:"rwaAmount"`
	QuoteAmount   string           `json:"quoteAmount" bson:"quoteAmount"`
	Status        RedemptionStatus `json:"status" bson:"status"`
	CreatedAt     int64            `json:"createdAt" bson:"createdAt"`
	TimeoutAt     int64            `json:"timeoutAt" bson:"timeoutAt"`
	ReasonCode    string           `json:"reasonCode,omitempty" bson:"reasonCode,omitempty"`
	RequestTxHash string           `json:"requestTxHash,omitempty" bson:"requestTxHash,omitempty"`
	FundTxHash    string           `json:"fundTxHash,omitempty" bson:"fundTxHash,omitempty"`
	// FundedAtBlock identifies RedemptionFunded for confirmations and claimable in the
	// API; it is 0 before funding
	FundedAtBlock uint64 `json:"-" bson:"fundedAtBlock"`
	// FundedAt records server indexing time for RedemptionFunded, which lacks a timestamp;
	// it is zero before funding
	FundedAt    time.Time `json:"-" bson:"fundedAt,omitempty"`
	ClaimTxHash string    `json:"claimTxHash,omitempty" bson:"claimTxHash,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt" bson:"updatedAt"`
	// Generation is rebuild bookkeeping, as in Purchase.Generation, which
	// redemption.Service.Reconcile stamps the same way
	Generation int64 `json:"-" bson:"generation"`
}
