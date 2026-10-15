// Package redemption reads request state derived only from indexed events
// (projector.go). Clients quote via RedemptionEscrow.previewRedeem; investor wallets
// submit request/claim/cancel, treasurers fund and redemption managers reject, subject
// to contract role checks. This package builds or submits none of these transactions.
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

// Service reads redemption requests and live compliance/preview state
type Service struct {
	client         blockchain.Client
	escrow         bindings.RedemptionEscrow
	escrowAddr     common.Address
	repo           repository.RedemptionRequestRepository
	registry       bindings.ComplianceRegistry
	complianceAddr common.Address
}

// New constructs a Service; zero complianceAddr makes IsBeneficiaryAllowed return false
// without a chain call
func New(client blockchain.Client, escrowAddr common.Address, repo repository.RedemptionRequestRepository, complianceAddr common.Address) *Service {
	return &Service{
		client: client, escrow: bindings.NewRedemptionEscrow(), escrowAddr: escrowAddr, repo: repo,
		registry: bindings.NewComplianceRegistry(), complianceAddr: complianceAddr,
	}
}

// IsBeneficiaryAllowed reads live ComplianceRegistry.isAllowed(address) for api
// Schemas.Redemption.beneficiaryAllowed, not eligibility at request time. Funding
// independently rechecks eligibility on execution.
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

// List returns all requests matching status (empty means all); API pagination uses
// ListPage
func (s *Service) List(ctx context.Context, status string) ([]*models.RedemptionRequest, error) {
	return s.repo.List(ctx, status)
}

// ListPage uses keyset pagination by newest CreatedAt, with optional status and
// case-insensitive beneficiary address filters. Empty cursor starts the first page;
// empty address disables that filter (see
// repository.RedemptionRequestRepository.ListPage).
func (s *Service) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.RedemptionRequest, string, error) {
	return s.repo.ListPage(ctx, status, address, cursor, limit)
}

// Get returns one redemption request by ID
func (s *Service) Get(ctx context.Context, id string) (*models.RedemptionRequest, error) {
	return s.repo.Get(ctx, id)
}

// Confirmations returns currentBlock - FundedAtBlock for Funded requests with a nonzero
// funding block no later than currentBlock, otherwise 0. currentBlock usually comes
// from the indexer checkpoint.
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
