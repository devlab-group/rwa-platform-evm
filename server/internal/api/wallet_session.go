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

// getMyWalletStatus handles GET /api/v1/me/wallet-status for the X-Wallet-Session
// address only. auth.RequireWalletSession rejects invalid sessions with 401; request
// parameters cannot select another wallet.
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
			// verifyChallenge upserts the investor before issuing a session. If the record is
			// later removed, return Unknown rather than an internal error.
			unknown := dto.WalletStatus{Address: address, Status: string(models.ComplianceUnknown), OwnershipVerified: true}
			c.JSON(http.StatusOK, unknown.WithFrozenTokens(app.currentProject(c)))
			return
		}
		failInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToWalletStatus(inv).WithFrozenTokens(app.currentProject(c)))
}

// currentProject loads the frozen-amount projection's project. A missing or unreadable
// record returns nil, omitting the amount without failing the status response.
func (app *App) currentProject(c *gin.Context) *models.Project {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		return nil
	}
	return p
}

// isAddressAllowed handles public GET /api/v1/compliance/allowed/{address}, returning
// only {allowed}. Redemptions.IsBeneficiaryAllowed reads ComplianceRegistry live, as
// for RedemptionResponse, rather than using stored investor status.
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
