package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/indexer"
)

// getConfig implements GET /api/v1/config (operationId getConfig).
// Public: returns only the bootstrap parameters the admin's web wallet needs to
// broadcast RWAFactory.deploy.
func (app *App) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, dto.NewBootstrapConfigResponse(app.ChainID, app.FactoryAddress, app.ProjectID))
}

// toProjectResponse supplies dto.ToProjectResponse with configured
// FinalityConfirmations and the stored profile's TokenUnit and legal data. Failed
// profile lookup falls back to the project's TokenUnit and omits legal.
func (app *App) toProjectResponse(ctx context.Context, p *models.Project) dto.ProjectResponse {
	tokenUnit := p.TokenUnit
	var legal *models.LegalDescriptor
	if profile, err := app.Repos.AssetProfiles.Get(ctx, p.ProjectID); err == nil {
		tokenUnit = profile.TokenUnit
		// Read legal from the validated profile without a separate storage field or migration
		var doc struct {
			Legal *models.LegalDescriptor `json:"legal"`
		}
		if json.Unmarshal(profile.ProfileRaw, &doc) == nil {
			legal = doc.Legal
		}
	}
	cp, cpErr := app.Repos.IndexerCheckpoints.Get(ctx, p.ChainID, indexer.CheckpointAddress)
	stale := securityStale(p.Security, cp, cpErr, time.Now().UTC())
	return dto.ToProjectResponse(p, tokenUnit, legal, app.FinalityConfirmations, stale)
}

// getEnforcement handles admin-only GET /api/v1/project/enforcement. Frozen wallets and
// the latest seizure are operational compliance data; GET /project is public. Investors
// read only their own frozen amount through GET /me/wallet-status.
func (app *App) getEnforcement(c *gin.Context) {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	cp, cpErr := app.Repos.IndexerCheckpoints.Get(c.Request.Context(), p.ChainID, indexer.CheckpointAddress)
	stale := securityStale(p.Security, cp, cpErr, time.Now().UTC())
	c.JSON(http.StatusOK, dto.ToEnforcementResponse(p, stale))
}

// securityStalenessWindow allows for the 15s security-reconcile interval and 5s indexer
// poll. A stalled indexer or projector freezes SecurityState.AsOfTime, making the
// response stale within a minute.
const securityStalenessWindow = 60 * time.Second

// securityStale reports stale when the projection or as-of time is absent, the indexer
// checkpoint is missing or unreadable, ReconciliationRequired indicates a paused
// indexer, or projection age exceeds securityStalenessWindow
func securityStale(s *models.SecurityState, cp *models.IndexerCheckpoint, cpErr error, now time.Time) bool {
	if s == nil || cpErr != nil || cp == nil {
		return true
	}
	if cp.ReconciliationRequired {
		return true
	}
	if s.AsOfTime.IsZero() {
		return true
	}
	return now.Sub(s.AsOfTime.UTC()) > securityStalenessWindow
}

// getProject handles public GET /api/v1/project, returning deployment addresses,
// lifecycle and projected governance/prices, or 404 before the project exists
func (app *App) getProject(c *gin.Context) {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, app.toProjectResponse(c.Request.Context(), p))
}
