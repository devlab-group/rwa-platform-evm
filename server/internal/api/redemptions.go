package api

import (
	"context"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/redemption"
)

// toRedemptionResponse derives claimable and confirmations from indexer height and
// finality depth. It reads beneficiaryAllowed from ComplianceRegistry.isAllowed; read
// failure reports false without failing the response.
func (app *App) toRedemptionResponse(ctx context.Context, r *models.RedemptionRequest) dto.RedemptionResponse {
	currentBlock := app.lastIndexedBlock()
	allowed := false
	if app.Redemptions != nil && common.IsHexAddress(r.Beneficiary) {
		if ok, err := app.Redemptions.IsBeneficiaryAllowed(ctx, common.HexToAddress(r.Beneficiary)); err == nil {
			allowed = ok
		}
	}
	return dto.ToRedemptionResponse(r,
		redemption.Claimable(r, currentBlock, app.FinalityConfirmations),
		redemption.Confirmations(r, currentBlock),
		allowed,
	)
}

// listRedemptions handles public GET /api/v1/redemptions using repository keyset
// pagination without X-Total-Count. Optional filters select status or beneficiary
// address, case-insensitively.
func (app *App) listRedemptions(c *gin.Context) {
	if app.Redemptions == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "redemptions is not configured on this server")
		return
	}
	status := c.Query("status")
	address := c.Query("address")
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Redemptions.ListPage(c.Request.Context(), status, address, cursor, limit)
	if err != nil {
		failInternal(c, err)
		return
	}
	out := make([]dto.RedemptionResponse, len(page))
	for i, r := range page {
		out[i] = app.toRedemptionResponse(c.Request.Context(), r)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// getRedemption implements GET /api/v1/redemptions/{id} (operationId
// getRedemption).
// Public: RedemptionEscrow.getRedemption(id) is an unauthenticated on-chain
// view, and ids are sequential, so an id grants no access.
func (app *App) getRedemption(c *gin.Context) {
	if app.Redemptions == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "redemptions is not configured on this server")
		return
	}
	r, err := app.Redemptions.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, app.toRedemptionResponse(c.Request.Context(), r))
}
