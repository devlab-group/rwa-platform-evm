// Package api implements the Gin HTTP handlers matching every operationId
// in api/openapi.yaml (FROZEN, lead-owned). Handlers are thin: validation
// and side effects live in the workflow packages (internal/assets,
// internal/compliance, internal/sales, internal/redemption,
// internal/project); this package only translates HTTP <-> those calls and
// enforces auth/idempotency/rate-limit middleware.
package api

import (
	"context"
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/metrics"
	"github.com/rwa-platform/server/internal/project"
	"github.com/rwa-platform/server/internal/redemption"
	"github.com/rwa-platform/server/internal/sales"
)

// challengeCreateRatePerSecond/challengeCreateBurst bound POST
// /compliance/challenge specifically, independent of app.RateLimitRPS
// (the deployment-wide limit, which may be disabled or configured looser).
// This endpoint is unauthenticated and each call always creates a new
// stored document, so it gets its own always-on, deliberately strict
// per-IP bucket regardless of global rate-limit configuration.
const (
	challengeCreateRatePerSecond = 0.5
	challengeCreateBurst         = 5
)

// sessionLoginRatePerSecond/sessionLoginBurst bound the admin auth endpoints
// POST /auth/challenge and POST /auth/session with an endpoint-specific login
// limiter that stays on independent of the optional global limiter. These are
// the credential-verification surface (a wallet-signature is verified against
// the admin address here — see createSession); with RATE_LIMIT_RPS<=0 (a
// valid, if inadvisable, production value that config.validateProductionBounds
// now refuses without an explicit override) rapid guessing would otherwise go
// completely unthrottled. Same always-on-regardless-of-global-config pattern as
// challengeCreateRatePerSecond.
const (
	sessionLoginRatePerSecond = 1
	sessionLoginBurst         = 10
)

// App bundles every dependency the API handlers need. All fields except
// Repos/ChainID may be nil in a reduced configuration (e.g. no compliance
// key configured); handlers that need a nil dependency return a documented
// 501/400 rather than panicking.
type App struct {
	Repos *repository.Repositories

	ChainID int64
	// FactoryAddress is the RWAFactory deploy entry point, surfaced verbatim by
	// GET /api/v1/config so the admin's web wallet can broadcast the deploy
	// itself (the server only observes ProjectDeployed). Empty when no factory
	// is configured.
	FactoryAddress string
	// ProjectID is the operator-configured UUID this deployment is pinned to.
	// It is surfaced by GET /api/v1/config so the admin web builds its Asset
	// Profile with this projectId instead of minting one, and createProfile
	// rejects any profile whose projectId differs (the safety gate). Empty
	// leaves the gate off (development/test).
	ProjectID        string
	LastIndexedBlock func() uint64 // nil-safe accessor; defaults to 0 if nil
	ReadyCheck       func() error  // nil means always ready

	Project     *project.Service
	Records     *assets.RecordService
	Challenges  *compliance.ChallengeService
	Webhooks    *compliance.WebhookService
	Status      *compliance.StatusService
	Sales       *sales.Service
	Redemptions *redemption.Service
	Audit       *auditlog.Logger
	// Sessions issues/validates the WalletSession bearer minted at
	// verifyChallenge. nil disables /api/v1/me/wallet-status
	// (auth.RequireWalletSession responds 501, not a panic — see its doc
	// comment) rather than every other handler needing its own nil check.
	Sessions *auth.SessionManager
	// AdminChallenges issues/verifies the single-use admin wallet-login
	// challenges backing POST /auth/challenge + /auth/session. nil makes both
	// return 501 not_configured (no admin can log in).
	AdminChallenges *auth.AdminChallengeService

	// FinalityConfirmations gates Redemption.claimable (api Schemas.Redemption).
	FinalityConfirmations uint64

	// Admin auth (wallet-signature -> JWT, single-admin model). AdminAddress is
	// the one wallet allowed to authenticate; JWTSecret signs/verifies the
	// issued admin JWT; JWTTTL bounds its lifetime. All three empty/zero
	// disable admin login (createSession returns 501).
	AdminAddress string
	JWTSecret    []byte
	JWTTTL       time.Duration

	IdempotencyTTL time.Duration
	RateLimitRPS   float64
	RateLimitBurst int
	// MaxRequestBodyBytes bounds every request body. <=0 disables the limit.
	MaxRequestBodyBytes int64
	// TrustedProxies is forwarded to gin.Engine.SetTrustedProxies. nil/empty
	// (the default) trusts NO proxies — see config.Config.TrustedProxies'
	// doc comment for why that, not gin's own permissive built-in default,
	// is what a directly internet-facing deployment needs.
	TrustedProxies []string
}

// recordAudit appends one entry to the operational audit trail, logging
// (never silently swallowing) a failure to do so. If these appends dropped
// their errors, a privileged transaction could be broadcast and returned as
// accepted while its actor/action record went missing. Every handler in this
// package that records a privileged action's audit entry should call this
// instead of `_ = app.Audit.Record(...)`, so a persistence failure for a
// compliance/mint/redemption action is at least OBSERVABLE (in server
// logs/monitoring) rather than invisible. This is a partial mitigation, not the
// stronger design (a transactional outbox that persists the audit/operation
// intent BEFORE broadcast and blocks on it) — retrofitting that across
// assets/compliance/sales without disturbing their existing, already-audited
// request flows needs dedicated design work beyond this pass.
func (app *App) recordAudit(ctx context.Context, category, actor, action, target string, metadata map[string]any) {
	if app.Audit == nil {
		return
	}
	if err := app.Audit.Record(ctx, category, actor, action, target, metadata); err != nil {
		log.Printf("api: audit record failed (category=%s action=%s target=%s actor=%s): %v", category, action, target, actor, err)
	}
}

// recordAuditIntent persists a durable pre-effect audit INTENT and returns its
// id. A privileged handler that broadcasts/mutates external
// state calls this BEFORE the effect and MUST treat a non-nil error as fatal —
// failing the request closed rather than acting with no durable actor/action
// trail. On success it later calls recordAuditResult with the returned id.
//
// A reduced deployment with no audit logger wired (app.Audit == nil) returns
// an empty id and nil: there is genuinely no durable store to fail closed
// against, matching every other optional-dependency degrade in this package
// (production config requires a real Mongo, so Audit is always wired there).
func (app *App) recordAuditIntent(ctx context.Context, category, actor, action, target string, metadata map[string]any) (string, error) {
	if app.Audit == nil {
		return "", nil
	}
	return app.Audit.RecordIntent(ctx, category, actor, action, target, metadata)
}

// recordAuditResult records the linked completion for a prior
// recordAuditIntent. A persistence failure is logged, never
// fatal: the intent already durably captured the actor/action, so the request
// itself has already succeeded or failed on its own merits by this point.
func (app *App) recordAuditResult(ctx context.Context, intentID, category, actor, action, target string, ok bool, metadata map[string]any) {
	if app.Audit == nil {
		return
	}
	if err := app.Audit.RecordResult(ctx, intentID, category, actor, action, target, ok, metadata); err != nil {
		log.Printf("api: audit result record failed (category=%s action=%s target=%s actor=%s ok=%v): %v", category, action, target, actor, ok, err)
	}
}

// NewRouter builds the Gin engine with every api/openapi.yaml route wired
// to its handler, plus RBAC/idempotency/rate-limit/size-limit/security-header
// middleware.
func NewRouter(app *App) *gin.Engine {
	r := gin.New()
	if err := r.SetTrustedProxies(app.TrustedProxies); err != nil {
		// Only returned for a malformed proxy entry (bad IP/CIDR) — a
		// config error, not a runtime condition to degrade gracefully
		// from: better to fail loudly at startup than silently trust
		// nothing (or, worse, panic later on first request).
		panic("api: invalid TrustedProxies: " + err.Error())
	}
	r.Use(gin.Recovery())
	r.Use(metrics.GinMiddleware())
	r.Use(auth.SecurityHeaders())
	r.Use(auth.MaxRequestBody(app.MaxRequestBodyBytes))
	if app.RateLimitRPS > 0 {
		r.Use(auth.RateLimit(app.RateLimitRPS, app.RateLimitBurst))
	}
	r.Use(auth.Authenticate(app.JWTSecret))

	idem := auth.Idempotency(app.Repos.Idempotency, app.IdempotencyTTL)

	r.GET("/healthz", app.getHealth)
	r.GET("/readyz", app.getReady)

	// Admin wallet login (BearerJwt scheme). Both steps are public (no role
	// gate — this is how the admin authenticates in the first place) and live
	// outside /api/v1, matching the SPA's /auth contract; both get an
	// always-on, strict per-IP limiter regardless of the global one. There is
	// no DELETE — the JWT is stateless, so logout is client-side.
	authLimiter := auth.RateLimit(sessionLoginRatePerSecond, sessionLoginBurst)
	r.POST("/auth/challenge", authLimiter, app.createAuthChallenge)
	r.POST("/auth/session", authLimiter, app.createSession)

	// adminOnly gates every route in the public/private matrix below that
	// must reject a missing/invalid admin JWT outright, instead of silently
	// downgrading it to RoleReadOnly the way auth.Authenticate does by default.
	// Without this, GET routes with no explicit role requirement would conflict
	// with the global auth rule. V1 is single-admin, so admin is the only
	// non-public role there is.
	adminOnly := auth.RequireRole(auth.RoleAdmin)

	v1 := r.Group("/api/v1")
	{
		v1.GET("/project", app.getProject)
		// getConfig is PUBLIC: it exposes only the chain id and factory address
		// the admin's web wallet needs to broadcast the deploy itself (the
		// server no longer deploys — it observes ProjectDeployed).
		v1.GET("/config", app.getConfig)

		v1.POST("/profile/validate", app.validateProfile)
		// admin-only, create-once (see createProfile) — the only supported
		// way to durably persist an Asset Profile.
		v1.POST("/profile", auth.RequireRole(auth.RoleAdmin), idem, app.createProfile)
		// getProfile lets the admin UI repopulate the Setup page after a reload
		// (the stored profile survived, but there was no way to fetch it) —
		// admin-only, same gating as createProfile; 404 when none exists yet.
		v1.GET("/profile", auth.RequireRole(auth.RoleAdmin), app.getProfile)

		// wallet/KYC history is operational compliance data, not
		// public market information — gated, unlike the investor
		// self-service challenge flow right below it (the caller there
		// only ever proves/queries ITS OWN address, nothing global).
		v1.GET("/compliance/wallets", adminOnly, app.listWallets)
		// An endpoint-specific limiter, ON TOP OF the
		// per-address active-challenge cap enforced inside
		// compliance.ChallengeService.Create — the limiter bounds request
		// RATE from a given source, the cap bounds STANDING storage per
		// address; neither alone is sufficient (a slow-but-steady caller
		// under the rate limit could still accumulate challenges without
		// the cap; a burst from many source IPs for the SAME address is
		// caught by the cap even though each IP individually stays under
		// its own rate limit).
		v1.POST("/compliance/challenge", auth.RateLimit(challengeCreateRatePerSecond, challengeCreateBurst), app.createChallenge)
		v1.POST("/compliance/challenge/verify", app.verifyChallenge)
		// getMyWalletStatus is subject-scoped by auth.RequireWalletSession
		// (X-Wallet-Session, minted at verifyChallenge — NOT an X-API-Key
		// role), so it is deliberately outside the adminOnly/RequireRole
		// vocabulary entirely, not merely "public" like the routes below.
		v1.GET("/me/wallet-status", auth.RequireWalletSession(app.Sessions), app.getMyWalletStatus)
		// isAddressAllowed is genuinely public (api/openapi.yaml:
		// security: []) — an anonymous transfer-preflight lookup that
		// discloses only {allowed}, never the full WalletStatus of a third
		// party. No RequireRole/RequireWalletSession here by design.
		v1.GET("/compliance/allowed/:address", app.isAddressAllowed)
		v1.GET("/compliance/webhooks", adminOnly, app.listWebhookEvents)
		v1.POST("/compliance/status", auth.RequireRole(auth.RoleAdmin), idem, app.setComplianceStatus)
		// kycWebhook is deliberately NOT API-key gated: it authenticates
		// via its own HMAC signature scheme (X-Webhook-Signature), not
		// X-API-Key — see the handler's doc comment.
		v1.POST("/compliance/webhook", app.kycWebhook)

		v1.GET("/audit-logs", adminOnly, app.listAuditLogs)

		// The full asset-record list and .rwa package download
		// (profile + metadata + typed data + proof material) are
		// operational/compliance data, gated; the preview-style GETs
		// elsewhere in this file stay public by design (see their own
		// comments).
		v1.GET("/assets/records", adminOnly, app.listRecords)
		v1.POST("/assets/records", auth.RequireRole(auth.RoleAdmin), idem, app.createRecord)
		v1.GET("/assets/records/:recordId/package", adminOnly, app.downloadPackage)
		// reissue is admin-only: re-attesting a stuck record is an audited
		// recovery action, narrower than the operator-or-admin create/sign
		// path above.
		v1.POST("/assets/records/:recordId/reissue", auth.RequireRole(auth.RoleAdmin), idem, app.reissueRecord)

		// sales/inventory stays public: market-facing figures an investor
		// reads before/without ever authenticating — the same on-chain state
		// is trivially readable by anyone directly from the chain regardless
		// of server-side gating. Per-amount purchase quotes are read straight
		// from Vault.previewBuy by the SPA, and the buy transaction itself is
		// built and signed client-side by the investor's wallet; the server
		// never assembles it.
		v1.GET("/sales/inventory", app.getInventory)
		// The full purchase history list is an operational record,
		// gated (unlike the inventory GET above).
		v1.GET("/sales/purchases", adminOnly, app.listPurchases)

		// The redemption list is PUBLIC: redemption requests are
		// on-chain-public data (the same set is trivially readable from the
		// chain, and a single redemption by :id is already public), so
		// gating the server's mirror provides no real confidentiality. Made
		// public so issuers can build their own investor UIs on top of it;
		// the optional `address` query filters to one beneficiary's
		// requests. A single redemption by :id likewise stays public:
		// RedemptionEscrow.getRedemption(id) is itself an unauthenticated
		// on-chain view call, and ids are sequential, not a capability token.
		// Redemption quotes come from RedemptionEscrow.previewRedeem read by
		// the SPA, and investor request/claim/cancel transactions are built
		// client-side by the wallet.
		v1.GET("/redemptions", app.listRedemptions)
		v1.GET("/redemptions/:id", app.getRedemption)

		// Server-submitted transaction history. PUBLIC (on-chain-public
		// data; issuers build their own investor UIs) with an optional
		// `address` query filtering to transactions touching that address.
		v1.GET("/transactions", app.listTransactions)
	}

	return r
}

func (app *App) lastIndexedBlock() uint64 {
	if app.LastIndexedBlock == nil {
		return 0
	}
	return app.LastIndexedBlock()
}
