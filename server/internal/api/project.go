package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/sales"
)

// getConfig implements GET /api/v1/config (operationId getConfig).
// Public: returns only the bootstrap parameters the admin's web wallet needs to
// broadcast RWAFactory.deploy.
func (app *App) getConfig(c *gin.Context) {
	version := ""
	if app.Project != nil {
		version = app.Project.FactoryVersion(c.Request.Context())
	}
	c.JSON(http.StatusOK, dto.NewBootstrapConfigResponse(app.ChainID, app.FactoryAddress, version, app.ProjectID))
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
	resp := dto.ToProjectResponse(p, tokenUnit, legal, app.FinalityConfirmations, stale)
	app.applyLivePricing(ctx, p, &resp)
	return resp
}

// livePricingTimeout bounds the strategy reads GET /project adds on top of the stored record
const livePricingTimeout = 3 * time.Second

// livePricingTTL is how long a live pricing result is reused, so the public GET /project
// does not turn into several eth_calls per request
const livePricingTTL = 15 * time.Second

// livePricing is one resolved applyLivePricing result
type livePricing struct {
	kind                 string
	oracle               *dto.OraclePriceResponse
	purchase, redemption string
	expires              time.Time
}

// pricingCache holds live pricing results by strategy address
type pricingCache struct {
	mu sync.Mutex
	m  map[common.Address]livePricing
	// group collapses concurrent misses for one strategy into one set of chain reads
	group singleflight.Group
}

// applyLivePricing uses cached chain reads to report the Vault's strategy kind and Oracle
// parameters/prices. Without a sales service or Active stack, stored values stand;
// identified Oracle strategies clear projected prices even when the price read fails.
func (app *App) applyLivePricing(ctx context.Context, p *models.Project, resp *dto.ProjectResponse) {
	if app.Sales == nil || p.Status != models.ProjectStatusActive || p.Addresses.Vault == "" {
		return
	}
	strategy := p.Addresses.Strategy
	if p.Security != nil && p.Security.Strategy != "" {
		strategy = p.Security.Strategy
	}
	if !common.IsHexAddress(strategy) {
		return
	}
	addr := common.HexToAddress(strategy)
	app.pricing.mu.Lock()
	hit, ok := app.pricing.m[addr]
	app.pricing.mu.Unlock()
	if ok && time.Now().Before(hit.expires) {
		resp.ApplyPricing(hit.kind, hit.oracle, hit.purchase, hit.redemption)
		return
	}

	v, _, _ := app.pricing.group.Do(addr.Hex(), func() (any, error) {
		return app.readLivePricing(ctx, p, strategy, addr), nil
	})
	res := v.(livePricing)
	resp.ApplyPricing(res.kind, res.oracle, res.purchase, res.redemption)
}

// readLivePricing caches strategy pricing for livePricingTTL, detaching reads from the
// request so one client disconnect cannot poison the shared result
func (app *App) readLivePricing(ctx context.Context, p *models.Project, strategy string, addr common.Address) livePricing {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), livePricingTimeout)
	defer cancel()
	kind := app.Sales.StrategyKind(ctx, addr)
	probed := kind != sales.KindUnknown
	// an RPC failure also probes as Unknown; the deploy-time kind was verified at adoption
	if !probed && p.StrategyKind != "" && strings.EqualFold(strategy, p.Addresses.Strategy) {
		kind = p.StrategyKind
	}
	res := livePricing{kind: kind, expires: time.Now().Add(livePricingTTL)}
	if kind == sales.KindOracle {
		if info, err := app.Sales.Oracle(ctx, addr); err == nil {
			res.oracle = oracleView(info)
		}
		prices := app.Sales.OraclePrices(ctx, addr)
		if prices.Purchase != nil {
			res.purchase = prices.Purchase.String()
		}
		if prices.Redemption != nil {
			res.redemption = prices.Redemption.String()
		}
	}
	// Cache unresolved results too: a public GET /project must not probe the chain on
	// every request while the strategy is unknown or the RPC is failing
	app.pricing.mu.Lock()
	if app.pricing.m == nil {
		app.pricing.m = map[common.Address]livePricing{}
	}
	app.pricing.m[addr] = res
	app.pricing.mu.Unlock()
	return res
}

// oracleView maps an OracleInfo to its API view, omitting an unset sequencer feed
func oracleView(info *sales.OracleInfo) *dto.OraclePriceResponse {
	v := &dto.OraclePriceResponse{
		Feed: info.Feed.Hex(), MaxAge: info.MaxAge, PurchaseSpreadBps: info.PurchaseSpreadBps,
		RedemptionSpreadBps: info.RedemptionSpreadBps, FeedDecimals: info.FeedDecimals,
		Available: info.Available, UpdatedAt: info.UpdatedAt, Error: info.Error,
	}
	if info.SequencerUptimeFeed != (common.Address{}) {
		v.SequencerUptimeFeed = info.SequencerUptimeFeed.Hex()
		v.GracePeriod = info.GracePeriod
	}
	if info.Price != nil {
		v.Price = info.Price.String()
	}
	return v
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
