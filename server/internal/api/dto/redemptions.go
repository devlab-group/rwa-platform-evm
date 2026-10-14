package dto

import (
	"github.com/rwa-platform/server/internal/dal/models"
)

// RedemptionResponse mirrors components.schemas.Redemption
type RedemptionResponse struct {
	ID          string `json:"id"`
	Beneficiary string `json:"beneficiary"`
	RWAAmount   string `json:"rwaAmount"`
	QuoteAmount string `json:"quoteAmount"`
	Status      string `json:"status"`
	Claimable   bool   `json:"claimable"`
	CreatedAt   int64  `json:"createdAt"`
	TimeoutAt   int64  `json:"timeoutAt"`
	// BeneficiaryAllowed is a live ComplianceRegistry.isAllowed(beneficiary) read.
	// The escrow re-checks the beneficiary when it funds, rejects or cancels a
	// request, so this is the current status, not the status at request time. A
	// failed chain read reports false instead of failing the response.
	BeneficiaryAllowed bool   `json:"beneficiaryAllowed"`
	Confirmations      uint64 `json:"confirmations"`
}

// ToRedemptionResponse maps one indexed redemption request onto its API view.
// claimable, confirmations and beneficiaryAllowed are computed by the caller:
// the first two from the indexer's block height and the configured finality
// depth, the third from a live chain read.
func ToRedemptionResponse(r *models.RedemptionRequest, claimable bool, confirmations uint64, beneficiaryAllowed bool) RedemptionResponse {
	return RedemptionResponse{
		ID: r.ID, Beneficiary: r.Beneficiary, RWAAmount: r.RWAAmount, QuoteAmount: r.QuoteAmount,
		Status: string(r.Status), CreatedAt: r.CreatedAt, TimeoutAt: r.TimeoutAt,
		Claimable: claimable, Confirmations: confirmations, BeneficiaryAllowed: beneficiaryAllowed,
	}
}
