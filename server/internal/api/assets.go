package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// validateProfile implements POST /api/v1/profile/validate (operationId
// validateProfile).
// Validates a profile without persisting it and returns the validation result
// with the derived digest and CID. The route is unauthenticated, so it must stay
// side-effect free; createProfile persists a profile.
func (app *App) validateProfile(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	result, _ := assets.ValidateProfile(raw)
	c.JSON(http.StatusOK, result)
}

// createProfile implements POST /api/v1/profile (operationId createProfile).
// Admin-only, create-once via AssetProfileRepository.Create: a projectId that
// already has a stored profile gets 409 and the stored profile is never
// overwritten.
func (app *App) createProfile(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	result, profile := assets.ValidateProfile(raw)
	if !result.Valid || profile == nil {
		c.JSON(http.StatusBadRequest, result)
		return
	}

	// When the operator configures a projectId, the profile must carry it, so a
	// stale or hand-crafted request cannot create a profile for another project
	if app.ProjectID != "" && profile.ProjectID != app.ProjectID {
		fail(c, http.StatusBadRequest, CodeBadRequest, "projectId does not match the server's configured project_id")
		return
	}

	err = app.Repos.AssetProfiles.Create(c.Request.Context(), &models.AssetProfile{
		ProjectID: profile.ProjectID, ProfileRaw: raw, Digest: result.ProfileDigest, CID: result.CID,
		TokenDecimals: profile.TokenDecimals, TokenUnit: profile.TokenUnit, RecordIDLabel: profile.RecordIDLabel,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			fail(c, http.StatusConflict, CodeConflict, "a profile already exists for this projectId and is immutable")
			return
		}
		failInternal(c, err)
		return
	}
	app.recordAudit(c.Request.Context(), "assets", caller(c), "assets.createProfile", profile.ProjectID, map[string]any{"profileDigest": result.ProfileDigest})
	c.JSON(http.StatusCreated, result)
}

// getProfile implements GET /api/v1/profile (operationId getProfile).
// Admin-only: the deployment's stored Asset Profile, or 404 when none exists
// yet.
func (app *App) getProfile(c *gin.Context) {
	p, err := app.Repos.AssetProfiles.GetCurrent(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToStoredProfileResponse(p))
}

// listRecords implements GET /api/v1/assets/records (operationId listRecords).
// Admin-only, keyset-paginated page of asset records. The query is bounded by
// AssetRecordRepository.ListPage, and X-Total-Count is omitted.
func (app *App) listRecords(c *gin.Context) {
	cursor, limit := cursorLimitParams(c)
	page, next, err := app.Repos.AssetRecords.ListPage(c.Request.Context(), cursor, limit)
	if err != nil {
		failInternal(c, err)
		return
	}
	out := make([]dto.AssetRecordResponse, len(page))
	for i, r := range page {
		out[i] = dto.ToAssetRecordResponse(r)
	}
	setPaginationHeaders(c, -1, len(out), next)
	c.JSON(http.StatusOK, out)
}

// CreateRecordRequestBody mirrors components.schemas.CreateRecordRequest
type CreateRecordRequestBody struct {
	RecordID string               `json:"recordId" binding:"required"`
	Asset    json.RawMessage      `json:"asset" binding:"required"`
	Amount   string               `json:"amount" binding:"required"`
	Proofs   []assets.ProofRecord `json:"proofs"`
}

// loadVerifiedProfile loads the project and its stored Asset Profile and checks
// that the profile's digest equals the project's profileDigest (the digest
// submitted to RWAFactory.deploy). Record and package operations use it so a
// replaced profile is never used to build packages or gate minting. The check
// is skipped while the project's ProfileDigest is empty, before a deployment is
// recorded.
func (app *App) loadVerifiedProfile(c *gin.Context) (*models.Project, *models.AssetProfile, bool) {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return nil, nil, false
	}
	stored, err := app.Repos.AssetProfiles.Get(c.Request.Context(), p.ProjectID)
	if err != nil {
		notFoundOrInternal(c, err)
		return nil, nil, false
	}
	if p.ProfileDigest != "" && !strings.EqualFold(p.ProfileDigest, stored.Digest) {
		failErr(c, http.StatusConflict, CodeConflict, fmt.Errorf(
			"api: stored asset profile digest %s does not match the deployed project's profileDigest %s", stored.Digest, p.ProfileDigest))
		return nil, nil, false
	}
	return p, stored, true
}

// currentProfile loads the project's digest-verified Asset Profile (see
// loadVerifiedProfile) and re-validates it to rebuild the compiled
// *assets.Profile the workflow layer needs. It re-validates on every call
// instead of caching, which is cheap at one profile per deployment.
func (app *App) currentProfile(c *gin.Context) (*assets.Profile, string, bool) {
	p, stored, ok := app.loadVerifiedProfile(c)
	if !ok {
		return nil, "", false
	}
	result, profile := assets.ValidateProfile(stored.ProfileRaw)
	if !result.Valid {
		failInternal(c, errors.New("api: stored asset profile no longer validates"))
		return nil, "", false
	}
	return profile, p.ProjectID, true
}

// createRecord implements POST /api/v1/assets/records (operationId
// createRecord).
// Admin-only: reserves a mint-eligible asset record (create-once), publishes its
// metadata, and returns the record the auditor's attestation is built from.
func (app *App) createRecord(c *gin.Context) {
	if app.Records == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "asset record workflow is not configured on this server")
		return
	}
	var body CreateRecordRequestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	profile, projectID, ok := app.currentProfile(c)
	if !ok {
		return
	}

	// Persist the audit intent before the record reservation and IPFS publication,
	// and abort if that fails, so no mint-eligible record exists without an audit trail
	actor := caller(c)
	intentID, err := app.recordAuditIntent(c.Request.Context(), "assets", actor, "assets.createRecord", body.RecordID, map[string]any{"amount": body.Amount})
	if err != nil {
		failInternal(c, err)
		return
	}
	rec, err := app.Records.CreateRecord(c.Request.Context(), profile, projectID, assets.CreateRecordRequest{
		RecordID: body.RecordID, Asset: body.Asset, Amount: body.Amount, Proofs: body.Proofs,
	})
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.createRecord", body.RecordID, false, map[string]any{"error": err.Error()})
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.createRecord", rec.RecordID, true, nil)
	c.JSON(http.StatusCreated, dto.ToAssetRecordResponse(rec))
}

// reissueRecord implements POST /api/v1/assets/records/{recordId}/reissue
// (operationId reissueRecord).
// Admin-only, audited recovery for a Pending record whose attestation window
// lapsed before it was signed. It assigns a fresh nonce and validUntil to the
// same record, so the operator can rebuild the package and the auditor can
// re-sign. A record that is not Pending (Signed, Minted, Rejected or Draft)
// gets 409, because rolling its nonce could strand or double-count a mint.
func (app *App) reissueRecord(c *gin.Context) {
	if app.Records == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "asset record workflow is not configured on this server")
		return
	}
	recordID := c.Param("recordId")
	// Persist the audit intent before the nonce and validUntil change, and abort if that fails
	actor := caller(c)
	intentID, err := app.recordAuditIntent(c.Request.Context(), "assets", actor, "assets.reissueRecord", recordID, nil)
	if err != nil {
		failInternal(c, err)
		return
	}
	rec, err := app.Records.ReissueRecord(c.Request.Context(), recordID)
	if err != nil {
		app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.reissueRecord", recordID, false, map[string]any{"error": err.Error()})
		switch {
		case errors.Is(err, assets.ErrRecordNotFound):
			failErr(c, http.StatusNotFound, CodeNotFound, err)
		case errors.Is(err, assets.ErrRecordNotReissuable):
			failErr(c, http.StatusConflict, CodeConflict, err)
		default:
			failInternal(c, err)
		}
		return
	}
	app.recordAuditResult(c.Request.Context(), intentID, "assets", actor, "assets.reissueRecord", rec.RecordID, true, nil)
	c.JSON(http.StatusOK, dto.ToAssetRecordResponse(rec))
}

// rejectedRecordError returns an error if rec is Rejected, the only status
// downloadPackage refuses, since a Rejected record is never minted
func rejectedRecordError(rec *models.AssetRecord) error {
	if rec.Status == models.RecordStatusRejected {
		return fmt.Errorf("record %s is Rejected and cannot be packaged or signed", rec.RecordID)
	}
	return nil
}

// downloadPackage implements GET /api/v1/assets/records/{recordId}/package
// (operationId downloadPackage).
// Admin-only: returns the .rwa zip for one record as application/zip. It holds
// the manifest, profile, metadata, typed data and any proofs, and is rebuilt
// deterministically from the stored record.
func (app *App) downloadPackage(c *gin.Context) {
	if app.Records == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "asset record workflow is not configured on this server")
		return
	}
	recordID := c.Param("recordId")
	rec, err := app.Repos.AssetRecords.Get(c.Request.Context(), recordID)
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	// Signed, Minted and expired records can still be downloaded, for audit
	// lookups after a mint. Only Rejected is refused.
	if err := rejectedRecordError(rec); err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
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

	zipBytes, err := app.Records.BuildPackage(c.Request.Context(), rec, profileDigestBytes, profileDoc.ProfileRaw)
	if err != nil {
		failInternal(c, err)
		return
	}
	c.Data(http.StatusOK, "application/zip", zipBytes)
}
