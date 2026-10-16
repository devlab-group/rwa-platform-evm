package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// CheckConfigDrift detects post-adoption wiring changes for an Active project without
// updating its record or status. cmd/platform logs findings, attempts audit writes and
// sets the drift metric.
//
// ReconcileSecurity supplies mutable treasury, auditor and Vault strategy expectations;
// the project record supplies immutable wiring, decimals, profile digest and timeout. A
// Vault strategy swap is reported because the escrow's immutable strategy then quotes
// redemptions through a different contract. Prices are excluded; role changes appear in
// Security.Roles rather than being compared with deploy-time holders.
//
// Returns (true, "", nil) on a match or when no Active project needs checking.
func CheckConfigDrift(ctx context.Context, client blockchain.Client, projects repository.ProjectRepository) (bool, string, error) {
	p, err := projects.Get(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("project: load project for config-drift check: %w", err)
	}
	if p.Status != models.ProjectStatusActive || p.Addresses.Token == "" {
		return true, "", nil
	}

	expected := driftExpectations(p)
	vaultStrategy := common.HexToAddress(expected.Addresses.Strategy)
	// Keep the deployed Addresses.Strategy for the escrow check; pass the live strategy
	// separately as the Vault expectation
	expected.Addresses.Strategy = p.Addresses.Strategy

	ok, reason, err := verifyImmutableConfig(ctx, client, expected, vaultStrategy)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, reason, nil
	}

	// rwa-v2 swaps split purchase/redemption pricing; rwa-v3 follows Vault.strategy(),
	// already checked by verifyImmutableConfig
	if p.Version != bindings.FactoryVersionV3 && vaultStrategy != common.HexToAddress(p.Addresses.Strategy) {
		return false, fmt.Sprintf("vault.strategy = %s but redemptionEscrow.strategy = %s (immutable): purchases and redemptions are priced by different contracts",
			vaultStrategy.Hex(), common.HexToAddress(p.Addresses.Strategy).Hex()), nil
	}
	return true, "", nil
}

// driftExpectations copies p, substitutes projected treasury, auditor and strategy
// values, and clears price expectations. All changed fields are strings in value
// structs, so the shallow copy leaves p unchanged.
func driftExpectations(p *models.Project) *models.Project {
	q := *p
	if s := p.Security; s != nil {
		if s.Treasury != "" {
			q.Treasury = s.Treasury
		}
		if s.Auditor != "" {
			q.Auditor = s.Auditor
		}
		if s.Strategy != "" {
			q.Addresses.Strategy = s.Strategy
		}
		if s.MinPurchaseQuote != "" {
			q.MinPurchaseQuote = s.MinPurchaseQuote
		}
	}
	// Empty expectations exclude legitimate pricer updates from drift checks;
	// ReconcileSecurity exposes live prices through Security in GET /project
	q.PurchasePricePerWholeToken = ""
	q.RedemptionPricePerWholeToken = ""
	return &q
}
