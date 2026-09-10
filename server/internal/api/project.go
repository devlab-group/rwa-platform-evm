package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/indexer"
)

// getConfig implements GET /api/v1/config (operationId getConfig).
// PUBLIC: returns only the bootstrap parameters the admin's web wallet needs to
// broadcast RWAFactory.deploy itself.
func (app *App) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, dto.NewBootstrapConfigResponse(app.ChainID, app.FactoryAddress, app.ProjectID))
}

// toProjectResponse gathers the values dto.ToProjectResponse cannot read off
// the project record and delegates the field mapping to it.
//
// FinalityConfirmations comes from the live server config
// (app.FinalityConfirmations) rather than whatever may be persisted on the
// project record: it is an operational chain-confirmation-depth setting, not an
// immutable on-chain value, so the running server's current configuration is
// always the correct source of truth. TokenUnit similarly comes from the live
// AssetProfile record (validateProfile persists it — see assets.go) rather than
// models.Project, since nothing else in the deploy flow carries tokenUnit.
func (app *App) toProjectResponse(ctx context.Context, p *models.Project) dto.ProjectResponse {
	tokenUnit := p.TokenUnit
	if profile, err := app.Repos.AssetProfiles.Get(ctx, p.ProjectID); err == nil {
		tokenUnit = profile.TokenUnit
	}
	cp, cpErr := app.Repos.IndexerCheckpoints.Get(ctx, p.ChainID, indexer.CheckpointAddress)
	stale := securityStale(p.Security, cp, cpErr, time.Now().UTC())
	return dto.ToProjectResponse(p, tokenUnit, app.FinalityConfirmations, stale)
}

// getEnforcement implements GET /api/v1/project/enforcement (operationId
// getEnforcement).
// ADMIN-ONLY, unlike GET /project. The fields here name individual wallets the
// issuer has frozen and the most recent seizure: operational compliance data,
// the same class as the wallet list, even though the events behind it are
// public on-chain. An investor learns their own frozen amount from the
// subject-scoped GET /me/wallet-status instead.
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

// securityStalenessWindow is how far the live governance projection may lag
// before /project reports securityStale. It is comfortably above the 15s
// security-reconcile ticker interval (and the 5s indexer poll), so a healthy
// system reports fresh while a stalled indexer OR a stopped projector — either
// of which freezes SecurityState.AsOfTime — trips stale within a minute.
const securityStalenessWindow = 60 * time.Second

// securityStale reports whether the governance projection can be trusted as
// current. It is stale when there is no projection yet, when the indexer
// checkpoint is unreadable/absent, when the indexer is frozen mid-reorg
// (ReconciliationRequired — the read models it feeds are deliberately not
// advancing), or when the projection's as-of time has fallen further behind
// now than securityStalenessWindow (a stalled indexer or a stopped projector).
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
// Returns the deployment's project record — addresses, lifecycle status, and
// the live governance/price projection — or 404 before one exists.
func (app *App) getProject(c *gin.Context) {
	p, err := app.Repos.Projects.Get(c.Request.Context())
	if err != nil {
		notFoundOrInternal(c, err)
		return
	}
	c.JSON(http.StatusOK, app.toProjectResponse(c.Request.Context(), p))
}
