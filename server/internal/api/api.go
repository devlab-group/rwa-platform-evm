// Package api serves the HTTP API with authentication, idempotency and request limits.
// Workflow services own asset, compliance, sales, redemption and project operations.
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
	"github.com/rwa-platform/server/internal/kyc"
	"github.com/rwa-platform/server/internal/metrics"
	"github.com/rwa-platform/server/internal/project"
	"github.com/rwa-platform/server/internal/redemption"
	"github.com/rwa-platform/server/internal/sales"
)

// Limit unauthenticated challenge creation per IP independently of the global limiter
const (
	challengeCreateRatePerSecond = 0.5
	challengeCreateBurst         = 5
)

// Keep per-IP admin-login limits enabled even when global rate limiting is disabled
const (
	sessionLoginRatePerSecond = 1
	sessionLoginBurst         = 10
)

// App holds handler dependencies; unconfigured optional services disable their endpoints
type App struct {
	Repos *repository.Repositories

	ChainID int64
	// ExplorerURL supplies transaction links; empty omits them
	ExplorerURL string
	// FactoryAddress is exposed by GET /api/v1/config for wallet-side deployment;
	// empty means no factory is configured
	FactoryAddress string
	// ProjectID pins profile creation to the operator's UUID and is exposed in config.
	// Empty disables the match requirement for development or tests.
	ProjectID        string
	LastIndexedBlock func() uint64 // nil-safe accessor; defaults to 0 if nil
	ReadyCheck       func() error  // nil means not ready

	Project    *project.Service
	Records    *assets.RecordService
	Burns      *assets.BurnService
	Challenges *compliance.ChallengeService
	Webhooks   *compliance.WebhookService
	Status     *compliance.StatusService
	// Guard checks Allowed submissions against cap and sanctions rules; nil disables these checks
	Guard *compliance.Guard
	// MonitoringBlock queues a Blocked decision for a monitoring hit on an unexpired Allowed wallet
	MonitoringBlock bool
	// KYC is the configured provider; nil disables startKYC and kycWebhook with 501
	KYC         kyc.Provider
	Sales       *sales.Service
	Redemptions *redemption.Service
	Audit       *auditlog.Logger
	// Sessions issues wallet-scoped tokens after ownership verification;
	// nil disables protected wallet-status access
	Sessions *auth.SessionManager
	// AdminChallenges supplies single-use login challenges; nil disables admin login
	AdminChallenges *auth.AdminChallengeService

	// FinalityConfirmations bounds when a funded redemption is exposed as claimable
	FinalityConfirmations uint64

	// AdminAddress identifies the login wallet; JWTSecret authenticates its tokens
	// and JWTTTL bounds their lifetime
	AdminAddress string
	JWTSecret    []byte
	JWTTTL       time.Duration

	IdempotencyTTL time.Duration
	RateLimitRPS   float64
	RateLimitBurst int
	// MaxRequestBodyBytes limits request bodies; non-positive values disable the limit
	MaxRequestBodyBytes int64
	// TrustedProxies configures forwarded-IP trust; empty trusts no proxies
	TrustedProxies []string
	// CORSAllowedOrigins lists permitted browser origins; empty disables CORS
	CORSAllowedOrigins []string
}

// recordAudit attempts an audit append and logs persistence failures.
// It is best effort; use recordAuditIntent when a durable record must precede an effect.
func (app *App) recordAudit(ctx context.Context, category, actor, action, target string, metadata map[string]any) {
	if app.Audit == nil {
		return
	}
	if err := app.Audit.Record(ctx, category, actor, action, target, metadata); err != nil {
		log.Printf("api: audit record failed (category=%s action=%s target=%s actor=%s): %v", category, action, target, actor, err)
	}
}

// recordAuditIntent persists intent before a privileged effect. Callers MUST abort on
// error and link completion with recordAuditResult. A reduced app without Audit returns
// an empty ID and no error.
func (app *App) recordAuditIntent(ctx context.Context, category, actor, action, target string, metadata map[string]any) (string, error) {
	if app.Audit == nil {
		return "", nil
	}
	return app.Audit.RecordIntent(ctx, category, actor, action, target, metadata)
}

// recordAuditResult links completion to a prior intent, logging failures without
// changing the already determined operation result
func (app *App) recordAuditResult(ctx context.Context, intentID, category, actor, action, target string, ok bool, metadata map[string]any) {
	if app.Audit == nil {
		return
	}
	if err := app.Audit.RecordResult(ctx, intentID, category, actor, action, target, ok, metadata); err != nil {
		log.Printf("api: audit result record failed (category=%s action=%s target=%s actor=%s ok=%v): %v", category, action, target, actor, ok, err)
	}
}

// NewRouter wires API handlers and authentication, idempotency and request-limit middleware
func NewRouter(app *App) *gin.Engine {
	r := gin.New()
	if err := r.SetTrustedProxies(app.TrustedProxies); err != nil {
		// Reject malformed proxy configuration at router construction
		panic("api: invalid TrustedProxies: " + err.Error())
	}
	r.Use(gin.Recovery())
	r.Use(metrics.GinMiddleware())
	r.Use(auth.SecurityHeaders())
	// Answer CORS preflights before authentication and rate limiting, while retaining
	// recovery, metrics and security headers
	r.Use(auth.CORS(app.CORSAllowedOrigins))
	r.Use(auth.MaxRequestBody(app.MaxRequestBodyBytes))
	if app.RateLimitRPS > 0 {
		r.Use(auth.RateLimit(app.RateLimitRPS, app.RateLimitBurst))
	}
	r.Use(auth.Authenticate(app.JWTSecret, app.AdminAddress))

	idem := auth.Idempotency(app.Repos.Idempotency, app.IdempotencyTTL)

	r.GET("/healthz", app.getHealth)
	r.GET("/readyz", app.getReady)

	// Login endpoints are public and independently rate-limited.
	// JWT logout is client-side; there is no DELETE session route.
	authLimiter := auth.RateLimit(sessionLoginRatePerSecond, sessionLoginBurst)
	r.POST("/auth/challenge", authLimiter, app.createAuthChallenge)
	r.POST("/auth/session", authLimiter, app.createSession)

	// adminOnly requires a valid admin JWT instead of anonymous read-only fallback
	adminOnly := auth.RequireRole(auth.RoleAdmin)

	v1 := r.Group("/api/v1")
	{
		v1.GET("/project", app.getProject)
		// Enforcement history is admin-only operational compliance data
		v1.GET("/project/enforcement", adminOnly, app.getEnforcement)
		// Public config supplies chain, factory and project identity for wallet-side setup
		v1.GET("/config", app.getConfig)

		// Validation compiles a caller-supplied schema, so it is admin-only like profile creation
		v1.POST("/profile/validate", adminOnly, app.validateProfile)
		// Profile creation is admin-only and create-once
		v1.POST("/profile", auth.RequireRole(auth.RoleAdmin), idem, app.createProfile)
		// Admin-only profile retrieval returns 404 until a profile is stored
		v1.GET("/profile", auth.RequireRole(auth.RoleAdmin), app.getProfile)

		// Global wallet and KYC history requires admin authentication
		v1.GET("/compliance/wallets", adminOnly, app.listWallets)
		// Per-IP rate limits bound creation frequency; ChallengeService's active cap
		// also bounds stored challenges per wallet
		v1.POST("/compliance/challenge", auth.RateLimit(challengeCreateRatePerSecond, challengeCreateBurst), app.createChallenge)
		v1.POST("/compliance/challenge/verify", app.verifyChallenge)
		// Wallet status requires its own X-Wallet-Session, not an admin JWT
		v1.GET("/me/wallet-status", auth.RequireWalletSession(app.Sessions), app.getMyWalletStatus)
		// Start verification for the session wallet, never a request-supplied address
		v1.POST("/compliance/kyc/start", auth.RequireWalletSession(app.Sessions), app.startKYC)
		// Public transfer preflight exposes allowed status without full wallet/KYC history
		v1.GET("/compliance/allowed/:address", app.isAddressAllowed)
		v1.GET("/compliance/webhooks", adminOnly, app.listWebhookEvents)
		v1.POST("/compliance/status", auth.RequireRole(auth.RoleAdmin), idem, app.setComplianceStatus)
		// Webhooks authenticate with the configured provider's raw-body signature
		v1.POST("/compliance/webhook", app.kycWebhook)

		v1.GET("/audit-logs", adminOnly, app.listAuditLogs)

		// Asset records and audit packages contain operational evidence and require admin access
		v1.GET("/assets/records", adminOnly, app.listRecords)
		v1.POST("/assets/records", auth.RequireRole(auth.RoleAdmin), idem, app.createRecord)
		v1.GET("/assets/records/:recordId/package", adminOnly, app.downloadPackage)
		// Reissuing a stuck record is an admin recovery action
		v1.POST("/assets/records/:recordId/reissue", auth.RequireRole(auth.RoleAdmin), idem, app.reissueRecord)

		// Burn package creation, download and reissue
		v1.GET("/assets/burns", adminOnly, app.listBurns)
		v1.POST("/assets/burns", auth.RequireRole(auth.RoleAdmin), idem, app.createBurn)
		v1.GET("/assets/burns/:burnId/package", adminOnly, app.downloadBurnPackage)
		v1.POST("/assets/burns/:burnId/reissue", auth.RequireRole(auth.RoleAdmin), idem, app.reissueBurn)

		// Inventory is public; wallets read purchase quotes and submit buys directly
		v1.GET("/sales/inventory", app.getInventory)
		// Global purchase history requires admin access
		v1.GET("/sales/purchases", adminOnly, app.listPurchases)

		// Redemption reads mirror public chain data, optionally filtered by beneficiary.
		// Wallets read quotes and submit request, claim and cancel calls directly.
		v1.GET("/redemptions", app.listRedemptions)
		v1.GET("/redemptions/:id", app.getRedemption)

		// Public transaction history includes server and indexed wallet transactions,
		// with an optional address filter
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
