// Package redemption implements redemption reads. Investor
// request/claim/cancel are wallet transactions the client builds directly
// against RedemptionEscrow; this package never builds or submits those, and
// the per-amount quote backing them is read from
// RedemptionEscrow.previewRedeem by the client too.
// Funding and rejection are likewise submitted directly by the
// treasurer/redemption-manager wallet in the web (RedemptionEscrow's onlyRole
// is the authorization). Status is reconstructed only from indexed chain
// events (see projector.go), never overridden by server workflow state.
package redemption

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Service reads the redemption request model and the live compliance and
// preview state backing it.
type Service struct {
	client         blockchain.Client
	escrow         bindings.RedemptionEscrow
	escrowAddr     common.Address
	repo           repository.RedemptionRequestRepository
	registry       bindings.ComplianceRegistry
	complianceAddr common.Address
}

// New constructs a redemption Service. complianceAddr may be the zero
// address if compliance is not configured; IsBeneficiaryAllowed then always
// returns false rather than making a doomed call.
func New(client blockchain.Client, escrowAddr common.Address, repo repository.RedemptionRequestRepository, complianceAddr common.Address) *Service {
	return &Service{
		client: client, escrow: bindings.NewRedemptionEscrow(), escrowAddr: escrowAddr, repo: repo,
		registry: bindings.NewComplianceRegistry(), complianceAddr: complianceAddr,
	}
}

// IsBeneficiaryAllowed calls ComplianceRegistry.isAllowed(address) live,
// backing api Schemas.Redemption.beneficiaryAllowed. Funding re-checks
// compliance, so this reflects current, not request-time, status.
func (s *Service) IsBeneficiaryAllowed(ctx context.Context, address common.Address) (bool, error) {
	if s.complianceAddr == (common.Address{}) {
		return false, nil
	}
	data, err := s.registry.PackIsAllowed(address)
	if err != nil {
		return false, err
	}
	out, err := blockchain.Call(ctx, s.client, s.complianceAddr, data)
	if err != nil {
		return false, fmt.Errorf("redemption: ComplianceRegistry.isAllowed: %w", err)
	}
	return s.registry.UnpackIsAllowed(out)
}

// List returns EVERY redemption request, optionally filtered by status
// (empty = all) — unbounded, for the internal aggregate consumers that
// genuinely need the whole set (cmd/platform's business-gauge refresh:
// pending count/oldest-age, funded-unclaimed count). API responses use
// ListPage instead.
func (s *Service) List(ctx context.Context, status string) ([]*models.RedemptionRequest, error) {
	return s.repo.List(ctx, status)
}

// ListPage returns one bounded, newest-CreatedAt-first page using
// repository-level keyset pagination — see
// repository.RedemptionRequestRepository.ListPage's doc comment. cursor
// is "" for the first page. address optionally filters to one beneficiary
// (empty = no filter), matched case-insensitively by the repository.
func (s *Service) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.RedemptionRequest, string, error) {
	return s.repo.ListPage(ctx, status, address, cursor, limit)
}

// Get returns one redemption request by ID.
func (s *Service) Get(ctx context.Context, id string) (*models.RedemptionRequest, error) {
	return s.repo.Get(ctx, id)
}

// Confirmations returns how many blocks have passed since r's
// RedemptionFunded event, given currentBlock (typically the indexer's
// last-scanned block). Zero for a request that isn't Funded yet.
func Confirmations(r *models.RedemptionRequest, currentBlock uint64) uint64 {
	if r.Status != models.RedemptionFunded || r.FundedAtBlock == 0 || currentBlock < r.FundedAtBlock {
		return 0
	}
	return currentBlock - r.FundedAtBlock
}

// Claimable implements api Schemas.Redemption's `claimable` convenience
// field: true only when status==Funded AND confirmations>=finalityConfirmations
// (architecture: "A Pending redemption is never a payment guarantee.").
func Claimable(r *models.RedemptionRequest, currentBlock, finalityConfirmations uint64) bool {
	return r.Status == models.RedemptionFunded && Confirmations(r, currentBlock) >= finalityConfirmations
}
