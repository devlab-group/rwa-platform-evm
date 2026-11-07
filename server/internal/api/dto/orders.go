package dto

import "github.com/rwa-platform/server/internal/dal/models"

// PurchaseOrderResponse mirrors components.schemas.PurchaseOrder
type PurchaseOrderResponse struct {
	ID             string `json:"id"`
	Buyer          string `json:"buyer"`
	Recipient      string `json:"recipient"`
	TokenAmount    string `json:"tokenAmount"`
	MaxQuoteAmount string `json:"maxQuoteAmount"`
	Strategy       string `json:"strategy"`
	// BaseRound and PricedRound are decimal strings because a feed round id is a uint80
	BaseRound string `json:"baseRound"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
	TimeoutAt int64  `json:"timeoutAt"`
	// PricedRound and QuoteAmount are set for Settled and Refunded orders; Refund for every
	// closed order. They are omitted while Pending.
	PricedRound   string `json:"pricedRound,omitempty"`
	QuoteAmount   string `json:"quoteAmount,omitempty"`
	Refund        string `json:"refund,omitempty"`
	RequestTxHash string `json:"requestTxHash"`
	CloseTxHash   string `json:"closeTxHash,omitempty"`
}

// ToPurchaseOrderResponse maps an indexed order
func ToPurchaseOrderResponse(o *models.PurchaseOrder) PurchaseOrderResponse {
	return PurchaseOrderResponse{
		ID:             o.ID,
		Buyer:          o.Buyer,
		Recipient:      o.Recipient,
		TokenAmount:    o.TokenAmount,
		MaxQuoteAmount: o.MaxQuoteAmount,
		Strategy:       o.Strategy,
		BaseRound:      o.BaseRound,
		Status:         string(o.Status),
		CreatedAt:      o.CreatedAt,
		TimeoutAt:      o.TimeoutAt,
		PricedRound:    o.PricedRound,
		QuoteAmount:    o.QuoteAmount,
		Refund:         o.Refund,
		RequestTxHash:  o.RequestTxHash,
		CloseTxHash:    o.CloseTxHash,
	}
}
