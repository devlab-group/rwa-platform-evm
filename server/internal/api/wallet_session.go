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
// Returns the WalletStatus for the address bound to the presented
// X-Wallet-Session, and only that address.
//
// auth.RequireWalletSession runs ahead of this handler and answers 401 for an
// invalid session, so no request parameter can select a different address.
func (app *App) getMyWalletStatus(c *gin.Context) {
	address, ok := auth.WalletSessionAddress(c)
	if !ok {
		// Unreachable behind RequireWalletSession; fail closed if it happens
		fail(c, http.StatusUnauthorized, CodeUnauthorized, "no wallet session")
		return
	}
	inv, err := app.Repos.Investors.Get(c.Request.Context(), address)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// ChallengeService.VerifyOwnership, the only path that issues a
			// session, upserts the Investor record first, so this happens only
			// if the record was removed after issuance. Report Unknown rather
			// than an internal error.
			unknown := dto.WalletStatus{Address: address, Status: string(models.ComplianceUnknown), OwnershipVerified: true}
			c.JSON(http.StatusOK, unknown.WithFrozenTokens(app.currentProject(c)))
			return
		}
		failInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToWalletStatus(inv).WithFrozenTokens(app.currentProject(c)))
}

// currentProject loads the project record for the frozen-amount lookup. It
// returns nil when the record is missing or unreadable, so the status response
// omits the frozen amount instead of failing.
func (app *App) currentProject(c *gin.Context) *models.Project {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		return nil
	}
	return p
}

// isAddressAllowed implements GET /api/v1/compliance/allowed/{address}
// (operationId isAddressAllowed).
// Public transfer preflight that discloses only {allowed}.
//
// It reads ComplianceRegistry.isAllowed(address) live through
// Redemptions.IsBeneficiaryAllowed, the same reader that fills
// RedemptionResponse.beneficiaryAllowed, rather than the stored Investor
// record.
func (app *App) isAddressAllowed(c *gin.Context) {
	addressParam := c.Param("address")
	if !common.IsHexAddress(addressParam) {
		fail(c, http.StatusBadRequest, CodeBadRequest, "address is not a valid hex address")
		return
	}
	if app.Redemptions == nil {
		// The route is public, so no credential could change this answer
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
