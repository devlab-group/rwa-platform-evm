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

// toRedemptionResponse computes the three enrichments the view mapper cannot
// derive from the record — claimable and confirmations against the indexer's
// block height and the configured finality depth, and a live
// ComplianceRegistry.isAllowed(beneficiary) read. A failed chain read leaves
// beneficiaryAllowed false rather than failing the whole response, since it is
// an enrichment, not the request's primary data.
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

// listRedemptions implements GET /api/v1/redemptions (operationId
// listRedemptions).
// PUBLIC (redemption requests are on-chain-public data, so issuers can build
// their own investor UIs), keyset-paginated, with optional `status` and
// `address` filters — the latter narrowing to one beneficiary's requests
// (case-insensitive).
//
// Uses repository-level keyset pagination
// (RedemptionRequestRepository.ListPage) — see api.listPurchases' doc comment
// for the same reasoning (bounded query, X-Total-Count omitted).
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
// PUBLIC: RedemptionEscrow.getRedemption(id) is itself an unauthenticated
// on-chain view call and ids are sequential, not a capability token.
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
