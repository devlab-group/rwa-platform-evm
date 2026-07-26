package models

import "time"

// RedemptionStatus mirrors IRedemptionEscrow.RedemptionStatus.
type RedemptionStatus string

const (
	RedemptionNone      RedemptionStatus = "None"
	RedemptionPending   RedemptionStatus = "Pending"
	RedemptionFunded    RedemptionStatus = "Funded"
	RedemptionCompleted RedemptionStatus = "Completed"
	RedemptionRejected  RedemptionStatus = "Rejected"
	RedemptionCancelled RedemptionStatus = "Cancelled"
)

// RedemptionRequest is a read model derived only from chain events
// (collection: redemption_requests). Server workflow data may annotate it
// but must never override chain-derived status.
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
	// FundedAtBlock is the block number of the RedemptionFunded event, used
	// to derive `confirmations`/`claimable` (api Schemas.Redemption); zero
	// until funded.
	FundedAtBlock uint64    `json:"-" bson:"fundedAtBlock"`
	ClaimTxHash   string    `json:"claimTxHash,omitempty" bson:"claimTxHash,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt" bson:"updatedAt"`
	// Generation mirrors Purchase.Generation — see its doc
	// comment. redemption.Service.Reconcile stamps it the same way.
	Generation int64 `json:"-" bson:"generation"`
}
