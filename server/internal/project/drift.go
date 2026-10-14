package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// CheckConfigDrift checks live wiring for an Active project without changing
// its record or status. Adoption verification runs once; this check detects
// later divergence. cmd/platform logs findings, writes audit entries and sets
// the drift metric.
//
// Mutable treasury, auditor and Vault strategy expectations come from
// ReconcileSecurity; immutable wiring, decimals, profile digest and timeout
// come from the project record. A Vault strategy swap still reports that
// purchases and redemptions use different contracts, because the escrow's
// strategy is immutable. Prices are excluded, and role changes are shown in
// Security.Roles instead of compared with deploy-time holders.
//
// Returns (true, "", nil) when values match or no Active project needs checking.
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
	// verifyImmutableConfig checks the escrow's strategy against
	// Addresses.Strategy, so that field keeps the deployed strategy while the
	// live one goes in as the Vault's expectation
	expected.Addresses.Strategy = p.Addresses.Strategy

	ok, reason, err := verifyImmutableConfig(ctx, client, expected, vaultStrategy)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, reason, nil
	}

	// After a swap the Vault and the RedemptionEscrow price through different
	// contracts, because RedemptionEscrow.strategy is immutable. That is not a
	// wiring fault, but an operator needs to know that purchases and redemptions
	// are no longer quoted by the same strategy.
	if vaultStrategy != common.HexToAddress(p.Addresses.Strategy) {
		return false, fmt.Sprintf("vault.strategy = %s but redemptionEscrow.strategy = %s (immutable): purchases and redemptions are priced by different contracts",
			vaultStrategy.Hex(), common.HexToAddress(p.Addresses.Strategy).Hex()), nil
	}
	return true, "", nil
}

// driftExpectations returns a copy of p that describes the chain as it should be
// now, not at deployment: the admin-mutable treasury, auditor and strategy take
// their live projected values, and the prices are cleared so they are not
// checked. The copy is shallow, but every field it rewrites is a string in a
// value struct, so p is unchanged.
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
	}
	// The pricer changes prices legitimately, so they are not drift.
	// verifyImmutableConfig skips an empty expectation, which excludes them here.
	// ReconcileSecurity projects the live values into Security, and GET /project
	// serves them.
	q.PurchasePricePerWholeToken = ""
	q.RedemptionPricePerWholeToken = ""
	return &q
}
