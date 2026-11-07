package models

import "time"

// PurchaseOrderStatus mirrors IVault.PurchaseStatus without None
type PurchaseOrderStatus string

const (
	PurchaseOrderPending   PurchaseOrderStatus = "Pending"
	PurchaseOrderSettled   PurchaseOrderStatus = "Settled"
	PurchaseOrderRefunded  PurchaseOrderStatus = "Refunded"
	PurchaseOrderCancelled PurchaseOrderStatus = "Cancelled"
	PurchaseOrderRejected  PurchaseOrderStatus = "Rejected"
)

// PurchaseOrder is an rwa-v4 forward-priced order rebuilt from Vault events in
// purchase_orders. Round IDs use decimal strings to preserve uint80 precision.
type PurchaseOrder struct {
	ID             string              `json:"id" bson:"_id"`
	Buyer          string              `json:"buyer" bson:"buyer"`
	Recipient      string              `json:"recipient" bson:"recipient"`
	TokenAmount    string              `json:"tokenAmount" bson:"tokenAmount"`
	MaxQuoteAmount string              `json:"maxQuoteAmount" bson:"maxQuoteAmount"`
	Strategy       string              `json:"strategy" bson:"strategy"`
	BaseRound      string              `json:"baseRound" bson:"baseRound"`
	Status         PurchaseOrderStatus `json:"status" bson:"status"`
	CreatedAt      int64               `json:"createdAt" bson:"createdAt"`
	// TimeoutAt is CreatedAt plus the project's purchase timeout
	TimeoutAt int64 `json:"timeoutAt" bson:"timeoutAt"`
	// PricedRound, QuoteAmount and Refund come from the closing event: PricedRound and
	// QuoteAmount for Settled and Refunded only, Refund for every closed status
	PricedRound   string `json:"pricedRound,omitempty" bson:"pricedRound,omitempty"`
	QuoteAmount   string `json:"quoteAmount,omitempty" bson:"quoteAmount,omitempty"`
	Refund        string `json:"refund,omitempty" bson:"refund,omitempty"`
	RequestTxHash string `json:"requestTxHash" bson:"requestTxHash"`
	// CloseTxHash is the transaction of the closing event; empty while Pending
	CloseTxHash string    `json:"closeTxHash,omitempty" bson:"closeTxHash,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt" bson:"updatedAt"`
	// Generation is rebuild bookkeeping, as in RedemptionRequest.Generation, which
	// sales.Service.ReconcileOrders stamps the same way
	Generation int64 `json:"-" bson:"generation"`
}
