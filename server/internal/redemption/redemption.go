// Package redemption implements redemption reads. The client builds the
// investor's request, claim and cancel transactions against RedemptionEscrow
// and reads the per-amount quote from RedemptionEscrow.previewRedeem. The
// treasurer's and redemption manager's wallets submit funding and rejection,
// and RedemptionEscrow's role checks authorize them. This package builds and
// submits none of these. A request's status is derived only from indexed chain
// events (see projector.go).
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

// New constructs a redemption Service. complianceAddr may be the zero address
// when compliance is not configured, in which case IsBeneficiaryAllowed returns
// false without making a call.
func New(client blockchain.Client, escrowAddr common.Address, repo repository.RedemptionRequestRepository, complianceAddr common.Address) *Service {
	return &Service{
		client: client, escrow: bindings.NewRedemptionEscrow(), escrowAddr: escrowAddr, repo: repo,
		registry: bindings.NewComplianceRegistry(), complianceAddr: complianceAddr,
	}
}

// IsBeneficiaryAllowed calls ComplianceRegistry.isAllowed(address) live and
// backs api Schemas.Redemption.beneficiaryAllowed. It reports the current
// status, not the status when the request was made. Funding checks the status
// again when it executes.
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

// List returns every redemption request, optionally filtered by status (empty
// means all), without pagination. API responses use ListPage.
func (s *Service) List(ctx context.Context, status string) ([]*models.RedemptionRequest, error) {
	return s.repo.List(ctx, status)
}

// ListPage returns one page of requests, newest CreatedAt first, using keyset
// pagination (see repository.RedemptionRequestRepository.ListPage). cursor is
// "" for the first page. address optionally filters to one beneficiary (empty
// means no filter) and is matched case-insensitively by the repository.
func (s *Service) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.RedemptionRequest, string, error) {
	return s.repo.ListPage(ctx, status, address, cursor, limit)
}

// Get returns one redemption request by ID.
func (s *Service) Get(ctx context.Context, id string) (*models.RedemptionRequest, error) {
	return s.repo.Get(ctx, id)
}

// Confirmations returns how many blocks have passed since r's RedemptionFunded
// event, given currentBlock (typically the indexer's last scanned block). It is
// zero unless r is Funded.
func Confirmations(r *models.RedemptionRequest, currentBlock uint64) uint64 {
	if r.Status != models.RedemptionFunded || r.FundedAtBlock == 0 || currentBlock < r.FundedAtBlock {
		return 0
	}
	return currentBlock - r.FundedAtBlock
}

// Claimable implements api Schemas.Redemption's `claimable` field: true only
// when the status is Funded and Confirmations is at least finalityConfirmations
func Claimable(r *models.RedemptionRequest, currentBlock, finalityConfirmations uint64) bool {
	return r.Status == models.RedemptionFunded && Confirmations(r, currentBlock) >= finalityConfirmations
}
