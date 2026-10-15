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
	// BeneficiaryAllowed reads ComplianceRegistry.isAllowed live, reflecting current
	// eligibility rather than request-time status. The escrow re-checks eligibility on
	// funding, rejection and cancellation. Failed reads report false without failing the
	// response.
	BeneficiaryAllowed bool   `json:"beneficiaryAllowed"`
	Confirmations      uint64 `json:"confirmations"`
}

// ToRedemptionResponse maps an indexed request. The caller derives claimable and
// confirmations from indexer height and finality depth, and beneficiaryAllowed from a
// live chain read.
func ToRedemptionResponse(r *models.RedemptionRequest, claimable bool, confirmations uint64, beneficiaryAllowed bool) RedemptionResponse {
	return RedemptionResponse{
		ID: r.ID, Beneficiary: r.Beneficiary, RWAAmount: r.RWAAmount, QuoteAmount: r.QuoteAmount,
		Status: string(r.Status), CreatedAt: r.CreatedAt, TimeoutAt: r.TimeoutAt,
		Claimable: claimable, Confirmations: confirmations, BeneficiaryAllowed: beneficiaryAllowed,
	}
}
