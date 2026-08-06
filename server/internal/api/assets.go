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
// A PURE validation check with no persistence side effect: it returns the
// validation result plus the derived digest/CID.
//
// It used to upsert every valid submission keyed by the document's own
// (attacker-controlled) projectId with no auth requirement — since a
// missing/invalid admin JWT reads as RoleReadOnly rather than being rejected (see
// auth.Authenticate), an unauthenticated caller could silently overwrite the
// active project's stored profile, poisoning every downstream
// record/package/signature operation. Use POST /api/v1/profile (admin-only,
// create-once — see createProfile) to actually persist one.
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
// already has a stored profile is rejected (409), never silently overwritten —
// the profile is immutable for the lifetime of its deployment.
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

	// Safety gate: when the operator has pinned a projectId in config, the
	// profile's projectId must match it. This fixes which deployment a profile
	// can belong to before the platform runs, so a stale or hand-crafted
	// request can't create a profile under the wrong projectId.
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
		// Propagate persistence failures to the caller instead of discarding
		// them — an earlier handler ignored Upsert's error entirely
		// (`_ = app.Repos.AssetProfiles.Upsert(...)`), so a storage failure
		// could report success while nothing was actually stored.
		failInternal(c, err)
		return
	}
	app.recordAudit(c.Request.Context(), "assets", caller(c), "assets.createProfile", profile.ProjectID, map[string]any{"profileDigest": result.ProfileDigest})
	c.JSON(http.StatusCreated, result)
}

// getProfile implements GET /api/v1/profile (operationId getProfile).
// Admin-only: the deployment's single persisted Asset Profile, or 404 when none
// exists yet.
func (app *App) getProfile(c *gin.Context) {
	p, err := app.Repos.AssetProfiles.GetCurrent(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.ToStoredProfileResponse(p))
}

// listRecords implements GET /api/v1/assets/records (operationId listRecords).
// Admin-only, keyset-paginated page of asset records.
//
// Uses repository-level keyset pagination (AssetRecordRepository.ListPage) —
// see api.listPurchases' doc comment for the same reasoning (bounded query,
// X-Total-Count omitted).
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

// CreateRecordRequestBody mirrors components.schemas.CreateRecordRequest.
type CreateRecordRequestBody struct {
	RecordID string               `json:"recordId" binding:"required"`
	Asset    json.RawMessage      `json:"asset" binding:"required"`
	Amount   string               `json:"amount" binding:"required"`
	Proofs   []assets.ProofRecord `json:"proofs"`
}

// loadVerifiedProfile loads the active project and its stored Asset
// Profile, and enforces the core cross-check: the stored profile's own
// digest must equal the project's persisted profileDigest (what was
// actually submitted to RWAFactory.deploy). Every record/package/signature
// operation goes through this instead of a bare AssetProfiles.Get, so even
// if some future bug reintroduces a stray write path to the profiles
// collection, a swapped-out profile is caught here rather than silently
// used to build packages or gate minting. p.ProfileDigest is empty before
// the first successful deploy (nothing to cross-check yet); the guard only
// activates once a real digest is on record.
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

// currentProfile loads and re-validates the project's stored (and
// digest-verified — see loadVerifiedProfile) Asset Profile, reconstructing
// the compiled *assets.Profile the workflow layer needs. Re-validating on
// every call is a simplification: caching the compiled jsonschema.Schema
// alongside the stored profile would avoid the repeat work, but V1's
// single-profile-per-deployment scale does not need it.
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
// Admin-only: reserves a mint-eligible asset record (create-once) and publishes
// its metadata, returning the record the auditor's attestation is built from.
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

	// Persist a durable audit INTENT before the external
	// effect (create-once record reservation + IPFS publication) and FAIL
	// CLOSED if it cannot be made durable, so a privileged mint-eligible
	// record can never come into existence with no actor/action trail.
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
// Admin-only audited recovery for a Pending record whose attestation window
// lapsed before it was signed: it assigns a fresh nonce/validUntil for the SAME
// immutable identity so the operator can rebuild the package and the auditor
// can re-sign, without violating record-ID uniqueness. A record that is not
// Pending (already Signed/Minted/Rejected/Draft) is a 409, not a silent
// nonce-roll that could strand or double-count a mint.
func (app *App) reissueRecord(c *gin.Context) {
	if app.Records == nil {
		fail(c, http.StatusNotImplemented, CodeNotImplemented, "asset record workflow is not configured on this server")
		return
	}
	recordID := c.Param("recordId")
	// Durable intent before the privileged nonce/validUntil mutation,
	// fail-closed.
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

// rejectedRecordError returns a non-nil error if rec is Rejected — the one
// status downloadPackage refuses regardless of expiry, since a Rejected record
// will never be minted and building anything for it is pointless work.
func rejectedRecordError(rec *models.AssetRecord) error {
	if rec.Status == models.RecordStatusRejected {
		return fmt.Errorf("record %s is Rejected and cannot be packaged or signed", rec.RecordID)
	}
	return nil
}

// downloadPackage implements GET /api/v1/assets/records/{recordId}/package
// (operationId downloadPackage).
// Admin-only: streams the deterministic .rwa zip (profile + metadata + typed
// data + proofs) for one record as application/zip.
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
	// downloadPackage never touches the chain — rebuilding/re-downloading
	// the deterministic .rwa bytes for a Signed/Minted/expired-but-otherwise-
	// valid record is harmless and remains useful as an audit-trail lookup
	// after mint. Only Rejected is refused: that record will never be minted,
	// so there is nothing legitimate to package.
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
