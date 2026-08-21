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

// CheckConfigDrift re-runs the post-deploy wiring verification against the
// live chain for an already-Active project, and reports what no longer
// matches.
//
// VerifyDeployment performs these checks exactly once, at adoption, and
// ReconcileDeployment short-circuits to a no-op for a project already Active
// on the same deploy transaction — so nothing re-evaluated them afterwards.
// An out-of-band rewiring (or a wiring value that silently stopped matching)
// therefore stayed invisible for the life of the deployment.
//
// It is READ-ONLY: it never touches the project record or its status. A
// deployment that has been legitimately rewired must not be demoted out of
// Active by a background ticker — that would take the whole API down over a
// condition an operator may have caused deliberately. Reporting is the
// caller's job (see cmd/platform's config-drift ticker: log + audit entry +
// metric).
//
// Legitimately mutable authority is compared against the LIVE event-sourced
// projection, not the deploy snapshot, so an admin's setTreasury/setAuditor/
// setStrategy is not reported as drift: those rotations are exactly what
// ReconcileSecurity exists to track. Everything else — the cross-contract
// wiring, decimals, the profile digest, the redemption timeout — is compared
// against the record, because nothing on chain can legitimately change it.
//
// Prices are excluded for the same reason (see driftExpectations).
//
// Roles are deliberately NOT re-checked here. verifyRoles compares against
// the deploy-time allowlist, so every legitimate later grant or revoke would
// register as drift; the live holder set is already projected into
// Security.Roles and surfaced by GET /project.
//
// Returns (true, "", nil) when everything still matches, or when there is no
// Active project to check.
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
	// verifyImmutableConfig reads Addresses.Strategy for the escrow's
	// immutable pointer, so hand it the deployed one there and the live one
	// as the Vault's expectation.
	expected.Addresses.Strategy = p.Addresses.Strategy

	ok, reason, err := verifyImmutableConfig(ctx, client, expected, vaultStrategy)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, reason, nil
	}

	// A swap leaves the Vault and the RedemptionEscrow pricing through
	// different contracts — RedemptionEscrow.strategy is immutable and can
	// never follow. Not a wiring fault, but a standing condition an operator
	// needs to see: a purchase and a redemption are no longer quoted by the
	// same strategy.
	if vaultStrategy != common.HexToAddress(p.Addresses.Strategy) {
		return false, fmt.Sprintf("vault.strategy = %s but redemptionEscrow.strategy = %s (immutable): purchases and redemptions are priced by different contracts",
			vaultStrategy.Hex(), common.HexToAddress(p.Addresses.Strategy).Hex()), nil
	}
	return true, "", nil
}

// driftExpectations returns a copy of p carrying what the chain should look
// like NOW rather than at deployment: admin-mutable authority takes the live
// projected value, and the prices are cleared so they are not checked at all.
// The copy is shallow, but every field it rewrites is a string inside a value
// struct, so p itself is untouched.
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
	// Prices move whenever the pricer says so — that is the strategy's whole
	// job, not drift. verifyImmutableConfig skips an empty expectation, which
	// is how they are excluded here without a mode flag. The live values are
	// projected into Security by ReconcileSecurity and served by GET /project.
	q.PurchasePricePerWholeToken = ""
	q.RedemptionPricePerWholeToken = ""
	return &q
}
