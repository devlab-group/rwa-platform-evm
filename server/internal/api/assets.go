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

// validateProfile handles admin-only POST /api/v1/profile/validate. It returns
// validation results, digest and CID without persisting anything; createProfile owns
// persistence.
func (app *App) validateProfile(c *gin.Context) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		failErr(c, http.StatusBadRequest, CodeBadRequest, err)
		return
	}
	result, _ := assets.ValidateProfile(raw)
	c.JSON(http.StatusOK, result)
}

// createProfile handles admin-only POST /api/v1/profile through
// AssetProfileRepository.Create. An existing projectId returns 409 without overwriting
// its profile.
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

// loadVerifiedProfile checks the stored Asset Profile against the project's
// ProfileDigest submitted to RWAFactory.deploy. Record and package operations reject a
// substituted profile. Before deployment is recorded, an empty ProfileDigest skips the
// check.
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

// currentProfile loads the digest-verified profile and re-validates it on each call to
// rebuild the workflow layer's compiled *assets.Profile
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

// createRecord handles admin-only POST /api/v1/assets/records, reserving a
// mint-eligible record once, publishing metadata and returning the record for auditor
// attestation
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

	// Persist intent before record reservation or IPFS publication; abort on failure so
	// every mint-eligible record has an audit trail
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

// reissueRecord handles admin-only, audited POST
// /api/v1/assets/records/{recordId}/reissue. It refreshes a Pending record's nonce and
// validUntil for a new package and signature, allowing recovery after expiry. Signed,
// Minted, Rejected and Draft records return 409; see assets.ErrRecordNotReissuable.
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

// downloadPackage handles admin-only GET /api/v1/assets/records/{recordId}/package. It
// rebuilds the record's deterministic .rwa archive as application/zip, including
// manifest, profile, metadata, typed data and proofs.
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
