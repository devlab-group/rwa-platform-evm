package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/dal/models"
)

// CreateBurnRequestBody mirrors components.schemas.CreateBurnRequest
type CreateBurnRequestBody struct {
	RecordID string               `json:"recordId" binding:"required"`
	Amount   string               `json:"amount" binding:"required"`
	Proofs   []assets.ProofRecord `json:"proofs"`
}

// burnView caches remaining per record, omitting it when computation fails
func (app *App) burnView(ctx context.Context, burn *models.Burn, cache map[string]string) dto.BurnResponse {
	remaining, ok := cache[burn.RecordID]
	if !ok {
		if rec, err := app.Repos.AssetRecords.Get(ctx, burn.RecordID); err == nil {
			if n, err := app.Burns.Remaining(ctx, rec, ""); err == nil {
				remaining = n.String()
			}
		}
		cache[burn.RecordID] = remaining
	}
	return dto.ToBurnResponse(burn, remaining)
}

// burnErrorStatus maps service errors to HTTP responses
func burnErrorStatus(c *gin.Context, err error) {
	switch {
	case errors.Is(err, assets.ErrRecordNotFound), errors.Is(err, assets.ErrBurnNotFound):
		failErr(c, http.StatusNotFound, CodeNotFound, err)
	case errors.Is(err, assets.ErrBurnRecordNotMinted), errors.Is(err, assets.ErrBurnExceedsRemaining),
		errors.Is(err, assets.ErrBurnAlreadyBurned), errors.Is(err, assets.ErrBurnConcurrentUpdate):
		failErr(c, http.StatusConflict, CodeConflict, err)
	case errors.Is(err, assets.ErrBurnInvalid):
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
	default:
		failInternal(c, err)
	}
}

// createBurn reserves an amount against a Minted record
func (app *App) createBurn(c *gin.Context) {
	if app.Burns == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "burn workflow is not configured on this server")
		return
	}
	var body CreateBurnRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	profile, projectID, ok := app.currentProfile(c)
	if !ok {
		return
	}
	actor := caller(c)
	intentID, err := app.recordAuditIntent(c.Request.Context(), "assets", actor, "assets.createBurn", body.RecordID, map[string]any{"amount": body.Amount})
	if err != nil {
		failInternal(c, err)
		return
	}
	burn, err := app.Burns.CreateBurn(c.Request.Context(), profile, projectID, assets.CreateBurnRequest{
		RecordID: body.RecordID, Amount: body.Amount, Proofs: body.Proofs,
	})
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.createBurn", body.RecordID, false, map[string]any{"error": err.Error()})
		burnErrorStatus(c, err)
		return
	}
	app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.createBurn", burn.BurnID, true, nil)
	c.JSON(http.StatusCreated, app.burnView(c.Request.Context(), burn, map[string]string{}))
}

// listBurns returns a page of burns, newest first, without X-Total-Count
func (app *App) listBurns(c *gin.Context) {
	if app.Burns == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "burn workflow is not configured on this server")
		return
	}
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Repos.Burns.ListPage(c.Request.Context(), cursor, limit)
	if err != nil {
		failInternal(c, err)
		return
	}
	cache := map[string]string{}
	out := make([]dto.BurnResponse, len(page))
	for i, b := range page {
		out[i] = app.burnView(c.Request.Context(), b, cache)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// downloadBurnPackage rebuilds the BurnAttestation .rwa archive in either status
func (app *App) downloadBurnPackage(c *gin.Context) {
	if app.Burns == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "burn workflow is not configured on this server")
		return
	}
	burn, err := app.Repos.Burns.Get(c.Request.Context(), c.Param("burnId"))
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	_, profileDoc, ok := app.loadVerifiedProfile(c)
	if !ok {
		return
	}
	profileDigestBytes, err := hexToBytes32(profileDoc.Digest)
	if err != nil {
		failInternal(c, err)
		return
	}
	zipBytes, err := app.Burns.BuildPackage(burn, profileDigestBytes, profileDoc.ProfileRaw)
	if err != nil {
		failInternal(c, err)
		return
	}
	c.Data(http.StatusOK, "application/zip", zipBytes)
}

// reissueBurn refreshes a Pending burn's nonce and expiry
func (app *App) reissueBurn(c *gin.Context) {
	if app.Burns == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "burn workflow is not configured on this server")
		return
	}
	burnID := c.Param("burnId")
	actor := caller(c)
	intentID, err := app.recordAuditIntent(c.Request.Context(), "assets", actor, "assets.reissueBurn", burnID, nil)
	if err != nil {
		failInternal(c, err)
		return
	}
	burn, err := app.Burns.ReissueBurn(c.Request.Context(), burnID)
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.reissueBurn", burnID, false, map[string]any{"error": err.Error()})
		burnErrorStatus(c, err)
		return
	}
	app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.reissueBurn", burn.BurnID, true, nil)
	c.JSON(http.StatusOK, app.burnView(c.Request.Context(), burn, map[string]string{}))
}
