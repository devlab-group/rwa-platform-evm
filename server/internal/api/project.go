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

// toProjectResponse gathers values outside the project record for dto.ToProjectResponse.
//
// FinalityConfirmations is an operational setting from the server configuration.
// TokenUnit and legal come from the stored AssetProfile; if lookup fails, TokenUnit
// falls back to the project record and legal is omitted.
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

// getEnforcement implements GET /api/v1/project/enforcement (operationId
// getEnforcement).
// Admin-only, unlike GET /project. It names the wallets the issuer has frozen
// and the most recent seizure, which is operational compliance data like the
// wallet list even though the events are public on-chain. An investor reads
// their own frozen amount from GET /me/wallet-status.
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

// securityStalenessWindow is how far the governance projection may lag before
// /project reports securityStale. It is well above the 15s security-reconcile
// interval and the 5s indexer poll, so a healthy server reports fresh, while a
// stalled indexer or stopped projector, either of which freezes
// SecurityState.AsOfTime, reports stale within a minute.
const securityStalenessWindow = 60 * time.Second

// securityStale reports whether the governance projection may be out of date.
// It is stale when there is no projection or as-of time, when the indexer
// checkpoint is unreadable or absent, when the checkpoint has
// ReconciliationRequired set (the indexer is paused), or when the as-of time is
// more than securityStalenessWindow behind now.
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

// getProject implements GET /api/v1/project (operationId getProject).
// Returns the deployment's project record (addresses, lifecycle status and the
// live governance and price projection), or 404 before one exists.
func (app *App) getProject(c *gin.Context) {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, app.toProjectResponse(c.Request.Context(), p))
}
