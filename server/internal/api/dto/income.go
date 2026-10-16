package dto

import (
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// IncomeDistributionResponse mirrors components.schemas.IncomeDistribution
type IncomeDistributionResponse struct {
	PreparationID  string `json:"preparationId"`
	Status         string `json:"status"`
	DistributionID string `json:"distributionId,omitempty"`
	MerkleRoot     string `json:"merkleRoot"`
	Total          string `json:"total"`
	RecordBlock    uint64 `json:"recordBlock"`
	// ClaimDeadline follows ClaimDeadlineExtended once the distribution exists
	ClaimDeadline   int64  `json:"claimDeadline"`
	SnapshotURI     string `json:"snapshotURI"`
	SnapshotSha256  string `json:"snapshotSha256"`
	EligibleSupply  string `json:"eligibleSupply"`
	Distributed     string `json:"distributed"`
	HolderCount     int    `json:"holderCount"`
	Claimed         string `json:"claimed,omitempty"`
	ReclaimedAmount string `json:"reclaimedAmount,omitempty"`
	CreateTxHash    string `json:"createTxHash,omitempty"`
	CreatedAt       string `json:"createdAt"`
}

// ToIncomeDistributionResponse maps a prepared snapshot and, once created on-chain, its
// distribution (nil while Prepared). The caller derives status from the deadline and the
// indexed events.
func ToIncomeDistributionResponse(p *models.IncomePreparation, d *models.IncomeDistribution, status string) IncomeDistributionResponse {
	resp := IncomeDistributionResponse{
		PreparationID: p.ID, Status: status, MerkleRoot: p.MerkleRoot, Total: p.Total, RecordBlock: p.RecordBlock,
		ClaimDeadline: p.ClaimDeadline, SnapshotURI: p.SnapshotURI, SnapshotSha256: p.SnapshotSha256,
		EligibleSupply: p.EligibleSupply, Distributed: p.Distributed, HolderCount: p.HolderCount,
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
	}
	if d != nil {
		resp.DistributionID = d.ID
		resp.ClaimDeadline = d.ClaimDeadline
		resp.Claimed = d.Claimed
		resp.ReclaimedAmount = d.ReclaimedAmount
		resp.CreateTxHash = d.CreateTxHash
	}
	return resp
}

// IncomeClaimResponse mirrors components.schemas.IncomeClaim
type IncomeClaimResponse struct {
	DistributionID string   `json:"distributionId"`
	Account        string   `json:"account"`
	Amount         string   `json:"amount"`
	Proof          []string `json:"proof"`
	MerkleRoot     string   `json:"merkleRoot"`
	ClaimDeadline  int64    `json:"claimDeadline"`
	SnapshotURI    string   `json:"snapshotURI"`
	Status         string   `json:"status"`
	ClaimTxHash    string   `json:"claimTxHash,omitempty"`
}

// ToIncomeClaimResponse maps one wallet's entitlement in distribution d. The caller supplies
// the account (EIP-55), amount and 0x-hex proof from the stored snapshot, and the status
// derived from indexed events.
func ToIncomeClaimResponse(d *models.IncomeDistribution, account, amount string, proof []string, status, claimTxHash string) IncomeClaimResponse {
	return IncomeClaimResponse{
		DistributionID: d.ID, Account: account, Amount: amount, Proof: proof, MerkleRoot: d.MerkleRoot,
		ClaimDeadline: d.ClaimDeadline, SnapshotURI: d.SnapshotURI, Status: status, ClaimTxHash: claimTxHash,
	}
}
