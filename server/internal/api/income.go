package api

import (
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/income"
)

// Error codes of the income endpoints
const (
	CodeInvalidRequest         = "invalid_request"
	CodeClaimWindowTooShort    = "claim_window_too_short"
	CodeIncomeUnsupported      = "income_unsupported"
	CodeRecordBlockNotFinal    = "record_block_not_final"
	CodeNoEligibleHolders      = "no_eligible_holders"
	CodeTransferHistoryMissing = "transfer_history_incomplete"
	CodeIPFSUnavailable        = "ipfs_unavailable"
	CodeAlreadyDistributed     = "already_distributed"
	CodeSnapshotTooLarge       = "snapshot_too_large"
)

// PrepareIncomeDistributionRequestBody mirrors components.schemas.PrepareIncomeDistributionRequest.
// Pointers tell an absent number from zero.
type PrepareIncomeDistributionRequestBody struct {
	RecordBlock   *int64  `json:"recordBlock"`
	Total         *string `json:"total"`
	ClaimDeadline *int64  `json:"claimDeadline"`
}

// incomeErrorStatus maps income service errors to HTTP responses
func incomeErrorStatus(c *gin.Context, err error) {
	switch {
	case errors.Is(err, income.ErrInvalidRequest):
		failErr(c, http.StatusBadRequest, CodeInvalidRequest, err)
	case errors.Is(err, income.ErrClaimWindowTooShort):
		failErr(c, http.StatusBadRequest, CodeClaimWindowTooShort, err)
	case errors.Is(err, income.ErrIncomeUnsupported):
		failErr(c, http.StatusConflict, CodeIncomeUnsupported, err)
	case errors.Is(err, income.ErrRecordBlockNotFinal):
		failErr(c, http.StatusConflict, CodeRecordBlockNotFinal, err)
	case errors.Is(err, income.ErrNoEligibleHolders):
		failErr(c, http.StatusConflict, CodeNoEligibleHolders, err)
	case errors.Is(err, income.ErrIncompleteHistory):
		failErr(c, http.StatusConflict, CodeTransferHistoryMissing, err)
	case errors.Is(err, income.ErrAlreadyDistributed):
		failErr(c, http.StatusConflict, CodeAlreadyDistributed, err)
	case errors.Is(err, income.ErrSnapshotTooLarge):
		failErr(c, http.StatusConflict, CodeSnapshotTooLarge, err)
	case errors.Is(err, income.ErrIPFSUnavailable):
		// the cause may name the IPFS host, so keep it out of the response
		fail(c, http.StatusServiceUnavailable, CodeIPFSUnavailable, "the snapshot could not be pinned to IPFS")
	default:
		failInternal(c, err)
	}
}

// prepareIncomeDistribution implements POST /api/v1/income/distributions
func (app *App) prepareIncomeDistribution(c *gin.Context) {
	if app.Income == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "income distribution is not configured on this server")
		return
	}
	var body PrepareIncomeDistributionRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeInvalidRequest, err)
		return
	}
	if body.RecordBlock == nil || *body.RecordBlock < 0 || body.Total == nil || body.ClaimDeadline == nil {
		fail(c, http.StatusBadRequest, CodeInvalidRequest, "recordBlock (non-negative), total and claimDeadline are required")
		return
	}
	req := income.PrepareRequest{RecordBlock: uint64(*body.RecordBlock), Total: *body.Total, ClaimDeadline: *body.ClaimDeadline}

	actor := caller(c)
	meta := map[string]any{"recordBlock": req.RecordBlock, "total": req.Total, "claimDeadline": req.ClaimDeadline}
	intentID, err := app.recordAuditIntent(c.Request.Context(), "income", actor, "income.prepareDistribution", "", meta)
	if err != nil {
		failInternal(c, err)
		return
	}
	prep, err := app.Income.Prepare(c.Request.Context(), req)
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "income", actor, "income.prepareDistribution", "", false, map[string]any{"error": err.Error()})
		incomeErrorStatus(c, err)
		return
	}
	app.recordAuditResult(c.Request.Context(), intentID, "income", actor, "income.prepareDistribution", prep.ID, true,
		map[string]any{"merkleRoot": prep.MerkleRoot, "snapshotSha256": prep.SnapshotSha256, "holderCount": prep.HolderCount})
	c.JSON(http.StatusCreated, dto.ToIncomeDistributionResponse(prep, nil, income.StatusPrepared))
}

// listIncomeDistributions implements GET /api/v1/income/distributions with an in-memory
// list sliced by offset cursor
func (app *App) listIncomeDistributions(c *gin.Context) {
	if app.Income == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "income distribution is not configured on this server")
		return
	}
	views, err := app.Income.List(c.Request.Context())
	if err != nil {
		failInternal(c, err)
		return
	}
	offset, limit := paginationParams(c)
	start, end, next := paginateWindow(len(views), offset, limit)
	page := views[start:end]
	out := make([]dto.IncomeDistributionResponse, len(page))
	for i, v := range page {
		out[i] = dto.ToIncomeDistributionResponse(v.Prep, v.Dist, v.Status)
	}
	setPaginationHeaders(c, len(views), len(out), next)
	c.JSON(http.StatusOK, out)
}

// listIncomeClaims implements public GET /api/v1/income/claims/{address}; snapshot
// addresses and entitlements are already published on IPFS
func (app *App) listIncomeClaims(c *gin.Context) {
	if app.Income == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "income distribution is not configured on this server")
		return
	}
	raw := c.Param("address")
	if !common.IsHexAddress(raw) || len(raw) != 42 {
		fail(c, http.StatusBadRequest, CodeBadRequest, "address must be a 0x-prefixed 20-byte hex address")
		return
	}
	views, err := app.Income.ClaimsFor(c.Request.Context(), common.HexToAddress(raw))
	if err != nil {
		failInternal(c, err)
		return
	}
	out := make([]dto.IncomeClaimResponse, len(views))
	for i, v := range views {
		proof := make([]string, len(v.Proof))
		for j, p := range v.Proof {
			proof[j] = "0x" + hex.EncodeToString(p[:])
		}
		out[i] = dto.ToIncomeClaimResponse(v.Dist, v.Account.Hex(), v.Amount.String(), proof, v.Status, v.ClaimTxHash)
	}
	c.JSON(http.StatusOK, out)
}
