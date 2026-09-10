package api

import (
	"errors"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// getMyWalletStatus implements GET /api/v1/me/wallet-status (operationId
// getMyWalletStatus).
// Returns the WalletStatus for ONLY the address bound to the presented
// X-Wallet-Session.
//
// auth.RequireWalletSession (wired ahead of this handler in api.go) has already
// validated the session and rejected with 401 before this handler ever runs, so
// there is no request parameter through which a caller could ask for a
// different address's status. This replaces the investor page's former
// dependency on the operator-only global wallet list.
func (app *App) getMyWalletStatus(c *gin.Context) {
	address, ok := auth.WalletSessionAddress(c)
	if !ok {
		// Unreachable given the route's middleware chain in api.go — fail
		// closed rather than silently proceeding if it somehow is.
		fail(c, http.StatusUnauthorized, CodeUnauthorized, "no wallet session")
		return
	}
	inv, err := app.Repos.Investors.Get(c.Request.Context(), address)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// compliance.ChallengeService.VerifyOwnership (the only path
			// that mints a session) always upserts an Investor record
			// before returning, so this is not expected in practice — but
			// respond with the honest "nothing on file yet" status rather
			// than an internal error if it's ever reached (e.g. the record
			// was independently removed after the session was issued).
			unknown := dto.WalletStatus{Address: address, Status: string(models.ComplianceUnknown), OwnershipVerified: true}
			c.JSON(http.StatusOK, unknown.WithFrozenTokens(app.currentProject(c)))
			return
		}
		failInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToWalletStatus(inv).WithFrozenTokens(app.currentProject(c)))
}

// currentProject loads the project record for the per-wallet frozen lookup,
// returning nil when there is none (or it cannot be read): a missing project
// must degrade to "no frozen amount reported", never to an error on a status
// call that is otherwise answerable.
func (app *App) currentProject(c *gin.Context) *models.Project {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		return nil
	}
	return p
}

// isAddressAllowed implements GET /api/v1/compliance/allowed/{address}
// (operationId isAddressAllowed).
// PUBLIC anonymous transfer-preflight lookup that discloses only {allowed}.
//
// The live ComplianceRegistry.isAllowed(address) read is via
// app.Redemptions.IsBeneficiaryAllowed — internal/redemption's existing
// on-chain reader (already used identically for
// Schemas.Redemption.beneficiaryAllowed in redemptions.go), not a
// stored/possibly-stale Investor snapshot. internal/compliance has no
// equivalent live single-address reader; reusing this one avoids adding a
// second on-chain isAllowed call path for the exact same contract read.
func (app *App) isAddressAllowed(c *gin.Context) {
	addressParam := c.Param("address")
	if !common.IsHexAddress(addressParam) {
		fail(c, http.StatusBadRequest, CodeBadRequest, "address is not a valid hex address")
		return
	}
	if app.Redemptions == nil {
		// This route is unauthenticated/public, so a 501 "not configured"
		// is the honest response here — there is no credential a caller
		// could present to get a different answer.
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "compliance eligibility lookup is not configured on this server")
		return
	}
	allowed, err := app.Redemptions.IsBeneficiaryAllowed(c.Request.Context(), common.HexToAddress(addressParam))
	if err != nil {
		failInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.AllowedResult{Allowed: allowed})
}
