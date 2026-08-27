// Command platform boots the self-hosted RWA platform server: it loads
// config, connects to MongoDB (falling back to an in-memory store if Mongo
// is unreachable, so local smoke-testing works without docker-compose) and
// to the configured chain RPC, wires every workflow service, and serves the
// Gin API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/rwa-platform/server/internal/alerts"
	"github.com/rwa-platform/server/internal/api"
	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/auth"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/mongodb"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/eip712"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
	"github.com/rwa-platform/server/internal/keys"
	"github.com/rwa-platform/server/internal/kyc"
	"github.com/rwa-platform/server/internal/metrics"
	"github.com/rwa-platform/server/internal/project"
	"github.com/rwa-platform/server/internal/redemption"
	"github.com/rwa-platform/server/internal/sales"
	"github.com/rwa-platform/server/internal/serverwiring"
	"github.com/rwa-platform/server/internal/txindex"
	"github.com/rwa-platform/server/internal/webui"

	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	configPath := flag.String("config", "", "path to the YAML configuration file (required)")
	flag.Parse()

	if *configPath == "" {
		log.Fatalf("config: --config <path> is required")
	}
	cfg, err := config.LoadFile(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	repos, mongoClient := connectRepositories(ctx, cfg)
	if mongoClient != nil {
		defer mongoClient.Disconnect(context.Background())
	}

	chainClient := connectChain(ctx, cfg)
	if chainClient != nil {
		defer chainClient.Close()
	} else if cfg.Environment == config.EnvProduction {
		// A production instance with no working chain RPC (dial failed,
		// ChainID check failed, or the endpoint is on the wrong chain — see
		// connectChain) has no functional privileged workflows. Fail fast
		// instead of coming up "healthy" with every chain-dependent service
		// silently disabled.
		log.Fatalf("platform: chain RPC unavailable or on the wrong chain; refusing to start in production without a working, correct-chain RPC (see connectChain / CHAIN_ID)")
	}

	// Config is bootstrap-only for the contract stack (only the factory
	// address lives there now): load every DEPLOYED address + the auditor
	// from the single Active DB Project record, kept live by the indexer
	// projector. A factory-only boot (no Active project yet) gets the zero
	// address set, leaving address-dependent services gated exactly as an
	// unset config address did before.
	loadCtx, loadCancel := context.WithTimeout(ctx, 5*time.Second)
	addrs, auditor := loadProjectAddresses(loadCtx, repos)
	loadCancel()

	app, initialProviders := buildApp(cfg, addrs, auditor, repos, chainClient, mongoClient)
	providers := &providerRegistry{}
	providers.add(initialProviders)

	router := api.NewRouter(app)
	webui.Attach(router)
	handler := &routerHandler{}
	handler.set(router)

	var bgLoops atomic.Pointer[context.CancelFunc]
	if chainClient != nil {
		bgCtx, cancel := context.WithCancel(ctx)
		bgLoops.Store(&cancel)
		startBackgroundLoops(bgCtx, cfg, addrs, repos, chainClient, app, mongoClient)
	} else {
		log.Println("platform: chain RPC unavailable at startup; indexer and tx-status loops are disabled until restart")
	}

	// Initialize/reconfigure dependent services from the active project
	// record: a factory-only boot (only FACTORY_ADDRESS
	// configured) has app.Records/Sales/Redemptions/Status all nil, since
	// buildApp wired them from environment addresses that don't exist yet.
	// Once the deployment reconciler (started inside startBackgroundLoops)
	// activates the project, rebuild the app/router/background loops from
	// the now-known addresses — no restart required. A no-op when the
	// server already has every address configured (nothing to watch for).
	if chainClient != nil && app.Project != nil {
		go watchProject(ctx, cfg, repos, chainClient, mongoClient, handler, providers, &bgLoops, addrs)
	}

	// Conservative read/write/idle timeouts and a header-size cap on
	// every listener — bare http.Server has none of these by default, which
	// is what makes an internet-facing listener vulnerable to slowloris-
	// style slow-client connection exhaustion.
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout, ReadTimeout: cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout, IdleTimeout: cfg.HTTPIdleTimeout,
		MaxHeaderBytes: cfg.HTTPMaxHeaderBytes,
	}

	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" {
		// Deliberately its own listener, not a route on the public API
		// router: /metrics is an operational surface for Prometheus, not
		// something to expose to arbitrary API callers.
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", metrics.Handler())
		metricsSrv = &http.Server{
			Addr: cfg.MetricsAddr, Handler: metricsMux,
			ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout, ReadTimeout: cfg.HTTPReadTimeout,
			WriteTimeout: cfg.HTTPWriteTimeout, IdleTimeout: cfg.HTTPIdleTimeout,
			MaxHeaderBytes: cfg.HTTPMaxHeaderBytes,
		}
		go func() {
			log.Printf("platform: metrics listening on %s", cfg.MetricsAddr)
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("platform: metrics server error: %v", err)
			}
		}()
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("platform: graceful shutdown error: %v", err)
		}
		if metricsSrv != nil {
			_ = metricsSrv.Shutdown(shutdownCtx)
		}
		// Zero every hot key's in-memory private key material now that the
		// process is shutting down and won't sign anything else (see
		// keys.Provider.Close's doc comment). providers
		// tracks every keys.Provider ever created, including ones replaced
		// by watchProject's rebuild (see providerRegistry's doc
		// comment) — not just the ones buildApp created at initial startup.
		providers.closeAll()
	}()

	log.Printf("platform: listening on %s (chainId=%d)", cfg.HTTPAddr, cfg.ChainID)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("platform: server error: %v", err)
	}
}

// routerHandler lets the public HTTP listener's *gin.Engine be swapped
// atomically once the active project record initializes the dependent
// services. A factory-only boot wires no
// Records/Sales/Redemptions/Status services (buildApp has no addresses to
// build them from yet), so once the deployment reconciler activates the
// project, every route depending on those services needs to start working
// WITHOUT a server restart. Swapping one atomic pointer here is far less
// invasive than making every api.App field individually swappable (~50
// read call sites across the api package) — see watchProject.
type routerHandler struct{ p atomic.Pointer[gin.Engine] }

func (h *routerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.p.Load().ServeHTTP(w, r) }
func (h *routerHandler) set(r *gin.Engine)                                { h.p.Store(r) }

// providerRegistry tracks every keys.Provider created over the process's
// lifetime — including ones watchProject's rebuild creates well
// after buildApp's initial call — so shutdown can zero every hot key's
// in-memory material exactly once each, not just the set
// buildApp happened to create at startup.
type providerRegistry struct {
	mu        sync.Mutex
	providers []keys.Provider
}

func (r *providerRegistry) add(ps []keys.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, ps...)
}

func (r *providerRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.providers {
		if err := p.Close(); err != nil {
			log.Printf("platform: closing key provider: %v", err)
		}
	}
}

// watchProject polls the project record and rebuilds the App/router/
// background loops whenever the address set they were wired from stops
// matching the record's. Two things move it:
//
//   - Activation. A factory-only boot starts with the zero address set and
//     every address-dependent service nil; once the deployment reconciler
//     (started inside startBackgroundLoops) drives the project to Active,
//     the discovered addresses are wired in without a restart. The
//     one-project-per-server invariant means this happens at most once.
//   - A strategy swap. Vault.setStrategy repoints the Vault at a different
//     pricing contract, and Security.Strategy follows it (see
//     project.ReconcileSecurity). The indexer's watched-address set and its
//     decoder are both fixed at construction, so the only way to scan the
//     new strategy's price events — and to type them rather than drop them
//     as generic — is to rebuild. Unlike activation this can recur, so this
//     loop keeps watching instead of returning after the first rewire.
//
// wired is the address set the caller already started background loops with.
func watchProject(ctx context.Context, cfg config.Config, repos *repository.Repositories, chainClient blockchain.Client, mongoClient *mongodriver.Client, handler *routerHandler, providers *providerRegistry, bgLoops *atomic.Pointer[context.CancelFunc], wired models.Addresses) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		p, err := repos.Projects.Get(ctx)
		if err != nil || p.Status != models.ProjectStatusActive {
			continue // repository.ErrNotFound, still Deploying/Verifying, or Failed (nothing to rewire for) — keep watching
		}
		addrs, auditor := addressesFromProject(p)
		if addrs == wired {
			continue
		}

		switch {
		case wired.Token == "":
			log.Printf("platform: project %s reached Active; rewiring dependent services from its discovered addresses", p.ProjectID)
		case addrs.Strategy != wired.Strategy:
			log.Printf("platform: project %s switched pricing strategy %s -> %s; rebuilding the indexer address set and dependent services", p.ProjectID, wired.Strategy, addrs.Strategy)
		default:
			log.Printf("platform: project %s address set changed; rewiring dependent services", p.ProjectID)
		}

		newApp, newProviders := buildApp(cfg, addrs, auditor, repos, chainClient, mongoClient)
		providers.add(newProviders)

		newRouter := api.NewRouter(newApp)
		webui.Attach(newRouter)
		handler.set(newRouter)

		if old := bgLoops.Load(); old != nil {
			(*old)() // cancel the loops reading the superseded App/address set
		}
		bgCtx, cancel := context.WithCancel(ctx)
		bgLoops.Store(&cancel)
		startBackgroundLoops(bgCtx, cfg, addrs, repos, chainClient, newApp, mongoClient)
		wired = addrs
	}
}

// loadProjectAddresses reads the deployed contract set + auditor for the
// single Active DB Project record (config is bootstrap-only — the deployed
// addresses and auditor live in the record, kept live by the indexer
// projector, NOT in config). It returns the zero address set (every
// address-dependent service then starts gated/deferred, exactly as an unset
// config address did before) whenever there is no Active project yet — a
// factory-only boot, a still-Deploying/Verifying project, or a Failed one —
// so the deployment reconciler + watchProject can wire services once
// the project reaches Active without a restart.
func loadProjectAddresses(ctx context.Context, repos *repository.Repositories) (models.Addresses, string) {
	p, err := repos.Projects.Get(ctx)
	if err != nil || p.Status != models.ProjectStatusActive {
		return models.Addresses{}, ""
	}
	return addressesFromProject(p)
}

// addressesFromProject extracts the deployed contract set + auditor from a
// Project record. The auditor is p.Auditor — the value captured at Deploy()
// time; buildApp additionally does a live SupplyController.auditor() read
// right after construction, and the ReconcileAuditor ticker in
// startBackgroundLoops keeps it current for any later on-chain rotation.
// AUDITOR_ADDRESS is no longer a competing config source at all: minting
// can't happen before a project is Active, and the record's Auditor is the
// sole authority.
func addressesFromProject(p *models.Project) (models.Addresses, string) {
	addrs := p.Addresses
	// Addresses.Strategy is the deploy baseline and is never rewritten;
	// Vault.setStrategy can point the Vault at a different pricing contract
	// afterwards. Security.Strategy is that live pointer, folded from
	// StrategyChanged by ReconcileSecurity — scan and decode THAT, or the
	// indexer keeps watching a strategy the Vault no longer prices through
	// (and a restart would silently do the same).
	if p.Security != nil && p.Security.Strategy != "" {
		addrs.Strategy = p.Security.Strategy
	}
	return addrs, p.Auditor
}

// connectRepositories resolves the persistence backend per
// cfg.PersistenceMode. The persistence mode is explicit; in-memory
// repositories are only permitted behind a development/test flag.
//
// PersistenceMode=="memory" is an explicit, intentional opt-in (config.Load
// refuses it in EnvProduction) — `go run ./cmd/platform` and local smoke
// tests can request it directly instead of relying on a silent fallback.
//
// PersistenceMode=="mongo" (the default) requires a reachable Mongo with
// successful index creation in EnvProduction: a connect/ping/index failure
// is fatal there, since silently continuing on in-memory repositories would
// lose project guards, idempotency records, KYC events, transaction
// tracking, audit logs, and indexer checkpoints on the next restart while
// still reporting ready. Outside
// production it still falls back with a loud warning, preserving the old
// no-docker-compose developer convenience.
func connectRepositories(ctx context.Context, cfg config.Config) (*repository.Repositories, *mongodriver.Client) {
	if cfg.PersistenceMode == "memory" {
		log.Printf("platform: PERSISTENCE_MODE=memory — using in-memory repositories (not durable; refused in production)")
		return memory.New(), nil
	}

	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client, err := dal.Connect(connectCtx, cfg.MongoURI)
	if err != nil {
		return fallbackOrFatal(cfg, "MongoDB connect failed", err)
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		return fallbackOrFatal(cfg, "MongoDB ping failed", err)
	}
	db := client.Database(cfg.MongoDB)
	if err := mongodb.EnsureIndexes(connectCtx, db); err != nil {
		if cfg.Environment == config.EnvProduction {
			log.Fatalf("platform: EnsureIndexes failed: %v (fatal in production with mongo persistence)", err)
		}
		log.Printf("platform: EnsureIndexes failed: %v", err)
	}
	log.Printf("platform: connected to MongoDB at %s (db=%s)", cfg.MongoURI, cfg.MongoDB)
	return mongodb.New(db), client
}

// fallbackOrFatal is connectRepositories' shared handling for a Mongo
// connect/ping failure: fatal in production, a logged in-memory fallback
// everywhere else (see connectRepositories' doc comment).
func fallbackOrFatal(cfg config.Config, reason string, err error) (*repository.Repositories, *mongodriver.Client) {
	if cfg.Environment == config.EnvProduction {
		log.Fatalf("platform: %s (%v); refusing to start on volatile in-memory repositories in production", reason, err)
	}
	log.Printf("platform: %s (%v); falling back to in-memory repositories (not allowed in production)", reason, err)
	return memory.New(), nil
}

func connectChain(ctx context.Context, cfg config.Config) *blockchain.RPCClient {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// serverwiring.DialChain dials AND verifies the
	// RPC-reported chain ID equals CHAIN_ID before returning — the EXACT same
	// check cmd/opsctl now performs before every chain-touching command. The
	// configured CHAIN_ID is what transactions are signed with and the
	// namespace the indexer labels logs under; an endpoint on a different
	// chain would sign for / query the wrong network while otherwise looking
	// healthy, so a mismatch (or dial/ChainID failure) leaves the server on
	// its chain-unavailable degrade path rather than acting against it.
	client, err := serverwiring.DialChain(dialCtx, cfg)
	if err != nil {
		log.Printf("platform: chain RPC unavailable (%v)", err)
		return nil
	}
	log.Printf("platform: connected to chain RPC at %s (chainId %d)", cfg.ChainRPCURL, cfg.ChainID)
	return client
}

// loadSigner resolves role's hot key through internal/keys per
// cfg.KeyProviderMode (local-keystore/vault/kms-mock, "raw"
// by default for backward compatibility — see internal/keys' package doc).
// rawHex is only consulted in "raw" mode. Returns nil (not an error) if the
// role has no key configured or the configured key is invalid: every hot
// key is optional, and callers already guard against a nil Signer where the
// corresponding action is attempted.
// loadSigner returns keys.Provider, not just blockchain.Signer, so callers
// building the App (buildApp) can collect every provider actually created
// and Close() them (zeroing key material) at shutdown; a
// keys.Provider is directly usable anywhere blockchain.Signer is expected
// regardless, since Provider's method set is a superset (see
// internal/keys.Provider's doc comment).
func loadSigner(role, rawHex string, cfg config.Config) keys.Provider {
	provider, err := keys.Load(role, keys.Config{
		Mode: keys.Mode(cfg.KeyProviderMode), RawHexKey: rawHex,
		KeystoreDir: cfg.KeystoreDir, PasswordFile: cfg.KeystorePasswordFile,
		VaultAddr: cfg.VaultAddr, VaultToken: cfg.VaultToken, VaultKeyPrefix: cfg.VaultKeyPrefix,
		KMSMockSeedHex: cfg.KMSMockSeedHex,
	})
	if err != nil {
		log.Printf("platform: hot key for role %q not configured (%v); leaving it unset", role, err)
		return nil
	}
	return provider
}

// collectSigner appends signer to *providers when non-nil — a keys.Provider
// value can be a non-nil interface wrapping a nil pointer is not possible
// here since loadSigner only ever returns either nil or a concrete
// constructor's result, so a plain nil check is sufficient (unlike the
// classic "typed nil in an interface" trap).
func collectSigner(providers *[]keys.Provider, signer keys.Provider) keys.Provider {
	if signer != nil {
		*providers = append(*providers, signer)
	}
	return signer
}

// newIPFSReplicationManager builds an ipfs.ReplicationManager over local
// plus every configured backup destination, or nil if no
// backup is configured — a bare local Kubo client alone provides no
// independent backup pinning destination, so callers fall back
// to using local directly rather than wrapping it in a ReplicationManager
// that could never reach PublicationReplicated. Called both when building
// the request-serving App (buildApp) and, separately, by
// startBackgroundLoops for the periodic retrieval-verification ticker — a
// fresh, stateless value each time, matching how this file already
// reconstructs blockchain.TxManager in both places rather than threading
// one shared instance through.
func newIPFSReplicationManager(cfg config.Config, local ipfs.Client, repos *repository.Repositories) *ipfs.ReplicationManager {
	var backups []ipfs.Destination
	if cfg.IPFSBackupArchiveDir != "" {
		archive, err := ipfs.NewFileArchiveClient(cfg.IPFSBackupArchiveDir)
		if err != nil {
			log.Fatalf("platform: IPFS_BACKUP_ARCHIVE_DIR: %v", err)
		}
		backups = append(backups, ipfs.Destination{Name: "archive", Client: archive})
	}
	if cfg.IPFSBackupKuboURL != "" {
		backups = append(backups, ipfs.Destination{Name: "backup-kubo", Client: ipfs.NewKuboClient(cfg.IPFSBackupKuboURL)})
	}
	if len(backups) == 0 {
		return nil
	}
	return ipfs.NewReplicationManager(local, backups, cfg.IPFSReplicationThreshold, repos.Publications)
}

// newTxManager builds the TxManager per cfg.TxCoordinationMode:
// "in-process" (default) constructs it with no NonceLeaseRepository
// involved at all; "mongo-lease" additionally wires repos.NonceLeases so
// concurrent replicas sharing this hot key coordinate through a
// distributed, fenced lease instead of relying on "exactly one process per
// hot key" (see TxManager's doc comment). Called both when building the
// request-serving App (buildApp) and, separately, by startBackgroundLoops
// for the periodic status-refresh ticker — a fresh, stateless value each
// time, matching how this file already reconstructs
// ipfs.ReplicationManager in both places rather than threading one shared
// instance through.
func newTxManager(cfg config.Config, chainClient blockchain.Client, repos *repository.Repositories) blockchain.TxManager {
	// The coordination-mode-aware construction lives in
	// serverwiring so cmd/opsctl's `tx replace` builds the IDENTICAL manager
	// (honoring TX_COORDINATION_MODE=mongo-lease fencing/TTL) instead of a
	// bare fee-cap manager that bypasses the shared nonce lease.
	return serverwiring.NewTxManager(cfg, chainClient, repos)
}

// buildApp wires every workflow service and returns the App plus every
// keys.Provider it created along the way, so main can Close() them all at
// shutdown (zeroing hot-key material in memory once the process is done
// with it — see keys.Provider.Close's doc comment).
func buildApp(cfg config.Config, addrs models.Addresses, auditor string, repos *repository.Repositories, chainClient blockchain.Client, mongoClient *mongodriver.Client) (*api.App, []keys.Provider) {
	var providers []keys.Provider
	app := &api.App{
		Repos:               repos,
		ChainID:             cfg.ChainID,
		FactoryAddress:      cfg.FactoryAddress,
		ProjectID:           cfg.ProjectID,
		AdminAddress:        cfg.AdminAddress,
		JWTSecret:           []byte(cfg.JWTSecret),
		JWTTTL:              cfg.JWTTTL,
		IdempotencyTTL:      cfg.IdempotencyTTL,
		RateLimitRPS:        cfg.RateLimitRPS,
		RateLimitBurst:      cfg.RateLimitBurst,
		CORSAllowedOrigins:  cfg.CORSAllowedOrigins,
		MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
		TrustedProxies:      cfg.TrustedProxies,
	}
	app.Audit = auditlog.New(repos.AuditLogs)

	if chainClient == nil {
		return app, providers // every chain-dependent service stays nil; handlers report 501 not_configured
	}

	txs := newTxManager(cfg, chainClient, repos)

	if cfg.FactoryAddress != "" {
		// No deployer key: RWAFactory.deploy is broadcast from the admin's
		// wallet and only OBSERVED here (project.ReconcileDeployment adopts the
		// ProjectDeployed event). The Service reads the chain and DB, never signs.
		// The admin address is what makes that observation trustworthy: the
		// factory is permissionless, so adoption is bound to a deploy
		// transaction the configured admin actually signed.
		app.Project = project.New(chainClient, common.HexToAddress(cfg.FactoryAddress), repos.Projects, cfg.ChainID,
			common.HexToAddress(cfg.AdminAddress))
	}

	auditorAddr := common.HexToAddress(auditor)
	if addrs.SupplyController != "" {
		domainCfg := eip712.Domain{
			Name: "RWA-Supply-Attestation", Version: "1",
			ChainID: big.NewInt(cfg.ChainID), VerifyingContract: common.HexToAddress(addrs.SupplyController),
		}
		var ipfsClient interface {
			AddRaw(ctx context.Context, data []byte) (string, error)
		}
		if cfg.IPFSAPIURL != "" {
			local := ipfs.NewKuboClient(cfg.IPFSAPIURL)
			if rm := newIPFSReplicationManager(cfg, local, repos); rm != nil {
				ipfsClient = rm
			} else {
				ipfsClient = local
			}
		}
		app.Records = assets.NewRecordService(
			repos.AssetRecords, repos.AuditPackages, repos.Attestations, ipfsClient,
			common.HexToAddress(addrs.SupplyController), common.HexToAddress(addrs.Vault),
			domainCfg, auditorAddr,
		)
		// Prefer a live SupplyController.auditor() read — the strongest
		// available source, since even p.Auditor (now flowing
		// through auditorAddr above via projectAddressesConfig) is only the
		// value captured at Deploy() time. Best-effort: a failed read just
		// leaves the constructor's value in place, corrected on the next
		// ReconcileAuditor tick (see startBackgroundLoops) once the indexer
		// has caught up, rather than blocking startup on an RPC call.
		liveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.Records.ReconcileAuditorLive(liveCtx, chainClient); err != nil {
			log.Printf("platform: live SupplyController.auditor() read failed, using %s: %v", auditorAddr, err)
		}
		cancel()
	}

	app.Challenges = compliance.NewChallengeService(repos.WalletChallenges, repos.Investors, cfg.WalletChallengeTTL, "RWA Platform")
	// A proven wallet mints a subject-scoped session at
	// verifyChallenge (see api.verifyChallenge). Wired whenever challenges
	// are — the two are a pair (challenge proves ownership, session carries
	// that proof to GET /me/wallet-status). Short TTL; storage is
	// repos.WalletSessions is a shared Mongo store in production, so a
	// session validates on every replica — see auth.SessionManager's
	// doc comment.
	app.Sessions = auth.NewSessionManager(repos.WalletSessions, cfg.WalletSessionTTL)
	// Admin wallet-login challenges back POST /auth/challenge + /auth/session
	// (see api.createSession). Storage is repos.AdminChallenges, same shared
	// Mongo store so a challenge issued by one replica verifies on another; the
	// challenge TTL reuses WalletChallengeTTL (a login challenge is the same
	// kind of short-lived proof-of-control artifact as the investor one).
	app.AdminChallenges = auth.NewAdminChallengeService(repos.AdminChallenges, cfg.WalletChallengeTTL, "RWA Platform")
	// Only wire the webhook service when a real secret is configured.
	// Leaving app.Webhooks nil makes kycWebhook return 501 not_configured
	// (matching every other optional-key feature's degrade path) instead of
	// running HMAC verification that VerifyHMAC would reject anyway —
	// config.Load already refuses to start in production without one; this
	// keeps development/test the same "feature is off" outcome without a
	// live, always-rejecting endpoint sitting on the router.
	// Select the KYC provider (none/sumsub/onfido) from config and wire it plus
	// its webhook processor. app.Webhooks — the provider-INDEPENDENT
	// replay/freshness/ownership/outbox half — is wired whenever ANY provider is
	// active, including the generic "none" provider, which kyc.New returns as
	// non-nil only when KYCWebhookHMACSecret is set (so the prior "webhook off
	// unless a secret is configured" behavior is preserved exactly).
	kycProvider, kerr := kyc.New(kyc.Config{
		Mode:              kyc.Mode(cfg.KYCProvider),
		GenericHMACSecret: cfg.KYCWebhookHMACSecret,
		Sumsub: kyc.SumsubConfig{
			AppToken: cfg.KYCSumsubAppToken, SecretKey: cfg.KYCSumsubSecretKey,
			WebhookSecret: cfg.KYCSumsubWebhookSecret, BaseURL: cfg.KYCSumsubBaseURL, LevelName: cfg.KYCSumsubLevelName,
		},
		Onfido: kyc.OnfidoConfig{
			APIToken: cfg.KYCOnfidoAPIToken, WebhookToken: cfg.KYCOnfidoWebhookToken,
			Region: cfg.KYCOnfidoRegion, WorkflowID: cfg.KYCOnfidoWorkflowID, Referrer: cfg.KYCOnfidoReferrer,
		},
	})
	if kerr != nil {
		// config.Load already validates the selected provider's required fields,
		// so this only fires on a genuine mismatch — degrade to "KYC disabled"
		// (endpoints report 501) rather than aborting the whole server.
		log.Printf("platform: KYC provider not configured (%v); KYC endpoints disabled", kerr)
	} else if kycProvider != nil {
		app.KYC = kycProvider
		app.Webhooks = compliance.NewWebhookService(repos.KYCEvents, repos.Investors, cfg.KYCWebhookHMACSecret)
	}
	if addrs.Compliance != "" {
		app.Status = compliance.NewStatusService(txs, common.HexToAddress(addrs.Compliance), collectSigner(&providers, loadSigner("compliance", cfg.ComplianceKeyHex, cfg)))
	}

	if addrs.Vault != "" {
		app.Sales = sales.New(chainClient, common.HexToAddress(addrs.Vault), common.HexToAddress(addrs.QuoteToken), repos.Purchases)
	}
	if addrs.RedemptionEscrow != "" {
		app.Redemptions = redemption.New(chainClient, common.HexToAddress(addrs.RedemptionEscrow), repos.RedemptionRequests, common.HexToAddress(addrs.Compliance))
	}

	app.FinalityConfirmations = cfg.Confirmations

	// Readiness must not check only the chain RPC: a production instance
	// running on the silent in-memory fallback could otherwise report ready
	// right up until restart wiped its state. Include a live Mongo ping
	// whenever a persistent backend is actually in use (mongoClient is nil
	// under PersistenceMode=="memory", where there's nothing to ping).
	app.ReadyCheck = func() error {
		// Re-verify the RPC is still on the configured chain on EVERY
		// readiness check, not just at startup — an endpoint that is
		// repointed/failed-over to a different chain must flip the instance to
		// not_ready before it signs or indexes against the wrong network.
		id, err := chainClient.ChainID(context.Background())
		if err != nil {
			return fmt.Errorf("chain: %w", err)
		}
		if id == nil || id.Int64() != cfg.ChainID {
			return fmt.Errorf("chain: RPC chainId %v != configured %d", id, cfg.ChainID)
		}
		if _, err := chainClient.BlockNumber(context.Background()); err != nil {
			return fmt.Errorf("chain: %w", err)
		}
		if mongoClient != nil {
			if err := mongoClient.Ping(context.Background(), nil); err != nil {
				return fmt.Errorf("storage: %w", err)
			}
		}
		// Readiness must fail while the indexer is unsafe: a deep
		// reorg the indexer could not auto-resolve leaves it paused
		// (ReconciliationRequired) with chain-derived read models frozen
		// mid-divergence; report not_ready rather than silently continuing
		// to serve stale/unsafe state as if everything were fine.
		if unsafe, err := indexerUnsafe(context.Background(), repos, cfg.ChainID); err != nil {
			return fmt.Errorf("indexer: %w", err)
		} else if unsafe {
			return errors.New("indexer: reconciliation required; chain-derived state is paused pending operator recovery")
		}
		return nil
	}
	app.LastIndexedBlock = func() uint64 {
		cp, err := repos.IndexerCheckpoints.Get(context.Background(), cfg.ChainID, indexer.CheckpointAddress)
		if err != nil {
			return 0
		}
		return cp.LastBlock
	}

	return app, providers
}

// startBackgroundLoops runs the indexer, transaction-status refresher, and
// the redemption/sales/assets/compliance read-model reconcilers on
// independent tickers until ctx is done.
func startBackgroundLoops(ctx context.Context, cfg config.Config, addrs models.Addresses, repos *repository.Repositories, chainClient blockchain.Client, app *api.App, mongoClient *mongodriver.Client) {
	// Drive the observe-only deployment projector: adopt a ProjectDeployed
	// event that binds to the stored profile and fully verify it (Undeployed->
	// Verifying->Active/Failed), or demote a project whose adopting event was
	// reorged out. A synchronous first call recovers whatever was mid-flight
	// when the process last stopped; the ticker keeps it current. Reads only
	// chain_events + the profile record — the server never broadcasts the deploy.
	if app.Project != nil {
		reconcileDeploy := func() {
			if err := app.Project.ReconcileDeployment(ctx, repos.ChainEvents, repos.AssetProfiles); err != nil {
				log.Printf("platform: project deployment reconcile error: %v", err)
			}
		}
		reconcileDeploy()
		go runTicker(ctx, 10*time.Second, reconcileDeploy)
	}

	// The scanned address set is derived from the DB Project record (config is
	// bootstrap-only) PLUS the configured factory address, so the factory's
	// ProjectDeployed event is ingested and the deployment projector above can
	// adopt it.
	addresses := serverwiring.IndexerAddresses(addrs, cfg.FactoryAddress)
	if len(addresses) > 0 {
		// WithBlockHashRepository enables walk-back-to-true-ancestor
		// reorg handling instead of the fixed-window fallback.
		idx := indexer.New(chainClient, repos.IndexerCheckpoints, repos.ChainEvents, cfg.ChainID, addresses, cfg.StartBlock, serverwiring.BuildDecoder(addrs, cfg.FactoryAddress),
			indexer.WithBlockHashRepository(repos.IndexerBlockHashes),
			// Commit each scanned chunk's events + block hashes +
			// pruning + checkpoint atomically (one Mongo transaction, majority
			// write concern), with the checkpoint advance conditional on its
			// stored version — so a crash mid-commit can't leave orphaned fork
			// events behind a still-old checkpoint, and two replicas can't
			// interleave a stale checkpoint over a newer one.
			indexer.WithChunkCommitter(repos.IndexerChunks),
			// Unprocessable logs are persisted for operator
			// retry/inspection instead of blocking the whole scan forever.
			indexer.WithDeadLetterQueue(repos.IndexerDeadLetters))
		go runTicker(ctx, 5*time.Second, func() {
			// context.Canceled is an expected teardown signal (this loop's ctx is
			// cancelled when watchProject rebuilds after the project reaches
			// Active, and on shutdown) — not a poll failure. Logging it as an
			// "indexer poll error" is misleading noise; the in-flight RPC to the
			// chain is simply abandoned and a fresh indexer resumes.
			if err := idx.Poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("platform: indexer poll error: %v", err)
			}
		})
	}

	if addrs.SupplyController != "" || addrs.Compliance != "" || addrs.Vault != "" {
		txs := newTxManager(cfg, chainClient, repos)
		// After a restart the transaction manager must reconstruct the state
		// of all submitted and pending transactions from MongoDB and the RPC
		// node. A synchronous first call —
		// mirroring the project deployment reconciler above — recovers
		// whatever was left mid-flight (mined-while-offline, dropped from
		// the mempool, an externally consumed nonce) before the ticker's
		// first 10s interval would otherwise leave it unresolved.
		if err := txs.RefreshStatuses(ctx, cfg.Confirmations); err != nil {
			log.Printf("platform: tx status refresh error: %v", err)
		}
		go runTicker(ctx, 10*time.Second, func() {
			if err := txs.RefreshStatuses(ctx, cfg.Confirmations); err != nil {
				log.Printf("platform: tx status refresh error: %v", err)
			}
		})
	}

	// Periodically verify that each configured provider can retrieve the
	// expected CID — a fetch +
	// digest-match against every destination of every known publication,
	// not just trusting each destination's original add/pin response.
	if cfg.IPFSAPIURL != "" {
		if rm := newIPFSReplicationManager(cfg, ipfs.NewKuboClient(cfg.IPFSAPIURL), repos); rm != nil {
			go runTicker(ctx, 30*time.Minute, func() {
				pubs, err := repos.Publications.List(ctx)
				if err != nil {
					log.Printf("platform: ipfs replication verify: list publications: %v", err)
					return
				}
				for _, p := range pubs {
					if err := rm.Verify(ctx, p.ID); err != nil {
						log.Printf("platform: ipfs replication verify %s: %v", p.ID, err)
					}
				}
			})
		}
	}

	// Every reconciler below reads only repos.ChainEvents (or
	// derived state) — gated via runIndexDependentTicker so none of them
	// run while the indexer itself is ReconciliationRequired.
	if app.Redemptions != nil && addrs.RedemptionEscrow != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "redemption reconcile", func() error {
			return app.Redemptions.Reconcile(ctx, repos.ChainEvents, cfg.ChainID, redemptionTimeoutSeconds(ctx, repos))
		})
	}

	if app.Sales != nil && addrs.Vault != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "sales reconcile", func() error {
			return app.Sales.Reconcile(ctx, repos.ChainEvents, cfg.ChainID)
		})
	}

	if app.Records != nil && addrs.SupplyController != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "assets minted-reconcile", func() error {
			return app.Records.ReconcileMinted(ctx, repos.ChainEvents, cfg.ChainID, addrs.SupplyController, repos.AssetRecords)
		})
		// Pick up an on-chain SupplyController.setAuditor
		// rotation performed outside this server (admin/multisig) without
		// requiring a restart — see RecordService.ReconcileAuditor's doc
		// comment.
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "assets auditor-reconcile", func() error {
			return app.Records.ReconcileAuditor(ctx, repos.ChainEvents, cfg.ChainID, addrs.SupplyController)
		})
	}

	if addrs.Compliance != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "compliance reconcile", func() error {
			return compliance.Reconcile(ctx, repos.ChainEvents, cfg.ChainID, addrs.Compliance, repos.Investors)
		})
	}

	// Project security-state projection (pause from Token, price from
	// Strategy) into the Project record, derived ONLY from indexed chain
	// events — gated like every other reconciler via runIndexDependentTicker.
	// Registered whenever the Token or Strategy address is known (an Active
	// project); ReconcileSecurity additionally self-skips a non-Active/
	// undeployed record, returning nil.
	if addrs.Token != "" || addrs.Strategy != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "project security-reconcile", func() error {
			return project.ReconcileSecurity(ctx, repos.Projects, repos.ChainEvents, repos.IndexerCheckpoints, cfg.ChainID)
		})
	}

	// Event-derived stack-transaction projection: synthesize a Transaction
	// record for every on-chain tx the server did NOT submit itself (wallet-
	// broadcast withdrawals, role changes, pauses, buys, redemption lifecycle,
	// admin transfers, compliance status) so GET /transactions lists them too.
	// Gated like every other reconciler; registered whenever any watched
	// contract address is set (self-skips when there are no events). Reads only
	// chain_events + transactions — see txindex.ReconcileStackTransactions.
	if addrs.Token != "" || addrs.Vault != "" || addrs.RedemptionEscrow != "" ||
		addrs.SupplyController != "" || addrs.Compliance != "" || addrs.Strategy != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "stack transactions-reconcile", func() error {
			return txindex.ReconcileStackTransactions(ctx, repos.Transactions, repos.ChainEvents, repos.IndexerCheckpoints, cfg.ChainID)
		})
	}

	// Drive Accepted->Applying->Applied/Failed for durably
	// queued webhook decisions. Runs whenever webhook ingestion is
	// enabled at all (app.Webhooks != nil), even if app.Status is nil —
	// WebhookReconciler.submit leaves those events visibly stuck at
	// Accepted rather than pretending they were applied; config.Load
	// fail-closes on that exact configuration in production. A
	// synchronous first call recovers whatever was Accepted/Applying when
	// the process last stopped, same as the deployment reconciler above.
	if app.Webhooks != nil {
		webhookReconciler := compliance.NewWebhookReconciler(repos.KYCEvents, repos.Transactions, app.Status)
		if err := webhookReconciler.Reconcile(ctx); err != nil {
			log.Printf("platform: webhook reconcile error: %v", err)
		}
		go runTicker(ctx, 10*time.Second, func() {
			if err := webhookReconciler.Reconcile(ctx); err != nil {
				log.Printf("platform: webhook reconcile error: %v", err)
			}
		})
	}

	// Re-verify the deployed stack's wiring against the chain. VerifyDeployment
	// runs these checks once, at adoption, and ReconcileDeployment is a no-op
	// for a project already Active on the same deploy tx — so without this
	// nothing ever re-evaluates them and an out-of-band rewiring stays
	// invisible. Read-only: it reports, it never demotes the project.
	if app.Project != nil && addrs.Token != "" {
		go runTicker(ctx, 5*time.Minute, func() { checkConfigDrift(ctx, repos, chainClient, app) })
	}

	go runTicker(ctx, 30*time.Second, func() { refreshBusinessGauges(ctx, repos, app) })
	go runTicker(ctx, 5*time.Minute, func() { evaluateAlerts(ctx, cfg, repos, app) })

	if mongoClient != nil {
		go runTicker(ctx, 30*time.Second, func() { checkStorageHealth(ctx, mongoClient, app) })
	}
}

// checkStorageHealth pings Mongo and reports degradation the same way
// evaluateAlerts reports an SLA breach: a log line, an audit-log entry, and
// a Prometheus counter increment, plus the rwa_storage_up gauge for
// dashboards/alertmanager so repository degradation is alertable.
// It only runs when a mongoClient is actually in play — see
// startBackgroundLoops' call site.
func checkStorageHealth(ctx context.Context, mongoClient *mongodriver.Client, app *api.App) {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := mongoClient.Ping(pingCtx, nil); err != nil {
		metrics.StorageUp.Set(0)
		log.Printf("platform: ALERT [storage_degraded] MongoDB ping failed: %v", err)
		metrics.AlertsFiredTotal.WithLabelValues("storage_degraded").Inc()
		if app.Audit != nil {
			_ = app.Audit.Record(ctx, "alerts", "system", "alerts.storage_degraded", "mongo", map[string]any{"error": err.Error()})
		}
		return
	}
	metrics.StorageUp.Set(1)
}

// refreshBusinessGauges recomputes the Prometheus business gauges
// (internal/metrics) from live repo/chain state. Best-effort: a failed
// read leaves the previous gauge value in place rather than zeroing it out
// (a stale-but-plausible dashboard value is more useful than a misleading
// "0 inventory" blip caused by one failed RPC call).
func refreshBusinessGauges(ctx context.Context, repos *repository.Repositories, app *api.App) {
	if app.Sales != nil {
		if inv, err := app.Sales.GetInventory(ctx); err == nil {
			if tokens, ok := new(big.Float).SetString(inv.Inventory); ok {
				whole, _ := new(big.Float).Quo(tokens, big.NewFloat(1e18)).Float64()
				metrics.SalesInventoryTokens.Set(whole)
			}
		}
	}

	requests, err := repos.RedemptionRequests.List(ctx, "")
	if err != nil {
		log.Printf("platform: business-gauge refresh: listing redemption requests: %v", err)
		return
	}
	now := time.Now().UTC()
	pendingCount, fundedUnclaimed := 0, 0
	oldestPendingAge := time.Duration(0)
	for _, r := range requests {
		switch r.Status {
		case models.RedemptionPending:
			pendingCount++
			if age := now.Sub(time.Unix(r.CreatedAt, 0).UTC()); age > oldestPendingAge {
				oldestPendingAge = age
			}
		case models.RedemptionFunded:
			fundedUnclaimed++
		}
	}
	metrics.RedemptionsPendingCount.Set(float64(pendingCount))
	metrics.RedemptionsPendingOldestAgeSeconds.Set(oldestPendingAge.Seconds())
	metrics.RedemptionsFundedUnclaimedCount.Set(float64(fundedUnclaimed))
}

// evaluateAlerts runs the alert evaluators (internal/alerts) against the
// current redemption read model and reports every finding: a log line, an
// audit-log entry (so it shows up on GET /api/v1/audit-logs alongside every
// other operational event), and a Prometheus counter increment.
func evaluateAlerts(ctx context.Context, cfg config.Config, repos *repository.Repositories, app *api.App) {
	requests, err := repos.RedemptionRequests.List(ctx, "")
	if err != nil {
		log.Printf("platform: alert evaluation: listing redemption requests: %v", err)
		return
	}
	now := time.Now().UTC()
	findings := alerts.EvaluatePendingRedemptionSLA(requests, cfg.PendingRedemptionSLA, now)
	findings = append(findings, alerts.EvaluateFundedClaimFailure(requests, cfg.FundedClaimFailureSLA, now)...)

	for _, a := range findings {
		log.Printf("platform: ALERT [%s] %s", a.Kind, a.Message)
		metrics.AlertsFiredTotal.WithLabelValues(a.Kind).Inc()
		if app.Audit != nil {
			_ = app.Audit.Record(ctx, "alerts", "system", "alerts."+a.Kind, a.RedemptionID, map[string]any{
				"message": a.Message, "since": a.Since, "ageSeconds": a.Age.Seconds(),
			})
		}
	}
}

// checkConfigDrift reports a deployed stack whose on-chain wiring no longer
// matches the project record, the same way evaluateAlerts reports an SLA
// breach: a log line, an audit-log entry and a Prometheus signal. The gauge
// is set on every outcome (including "no drift"), so a deployment that is put
// back in order clears it without waiting for a restart.
func checkConfigDrift(ctx context.Context, repos *repository.Repositories, chainClient blockchain.Client, app *api.App) {
	ok, reason, err := project.CheckConfigDrift(ctx, chainClient, repos.Projects)
	if err != nil {
		// An RPC failure is not drift — leave the gauge where it was rather
		// than reporting a clean stack as broken (or a broken one as clean).
		log.Printf("platform: config-drift check: %v", err)
		return
	}
	if ok {
		metrics.ConfigDrift.Set(0)
		return
	}
	metrics.ConfigDrift.Set(1)
	log.Printf("platform: ALERT [config_drift] %s", reason)
	metrics.AlertsFiredTotal.WithLabelValues("config_drift").Inc()
	if app.Audit != nil {
		_ = app.Audit.Record(ctx, "alerts", "system", "alerts.config_drift", "project", map[string]any{"reason": reason})
	}
}

// redemptionTimeoutSeconds reads the immutable per-deployment redemption
// timeout from the project record, falling back to the recommended 14-day
// default if the project hasn't finished deploying yet.
func redemptionTimeoutSeconds(ctx context.Context, repos *repository.Repositories) int64 {
	const defaultTimeout = 14 * 24 * 60 * 60
	p, err := repos.Projects.Get(ctx)
	if err != nil || p.RedemptionTimeout == 0 {
		return defaultTimeout
	}
	return p.RedemptionTimeout
}

func runTicker(ctx context.Context, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}

// indexerUnsafe reports whether chainID's indexer checkpoint is currently
// ReconciliationRequired. The persisted models.IndexerCheckpoint carries
// the shared indexer safety state: LastBlock/LastBlockHash are the canonical
// height/hash the indexer last trusted, UpdatedAt is the last successful
// poll, and ReconciliationRequired is the flag itself. Lag (current head
// minus LastBlock) is left to callers that need it, computed from
// BlockNumber() rather than this helper making an extra RPC call on every
// check. No checkpoint yet is NOT unsafe — that's the
// normal state at/just after startup before the indexer's first
// successful Poll, not a divergence.
func indexerUnsafe(ctx context.Context, repos *repository.Repositories, chainID int64) (bool, error) {
	cp, err := repos.IndexerCheckpoints.Get(ctx, chainID, indexer.CheckpointAddress)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return cp.ReconciliationRequired, nil
}

// runIndexDependentTicker is runTicker plus an indexer-safety gate: fn is
// skipped entirely (not called) for any tick where indexerUnsafe reports
// true — every read model fn depends on is frozen mid-divergence at that
// point (the indexer itself has already stopped advancing chain_events —
// see indexer.Indexer.Poll's ErrReconciliationRequired short-circuit), so
// running fn would at best repeat stale work and at worst act on
// automated write side effects (redemption settlement, compliance status,
// asset minted-tracking, auditor rotation) derived from a not-yet-
// reconciled chain view.
// runIndexDependentTicker's fn returns its error instead of logging it
// itself so this wrapper can both log AND record it on
// metrics.ReadModelReconcileErrorsTotal in one place — a single
// reconciliation health/error metric for every reconciler that goes through
// it, rather than repeating that at each of the five call sites in
// startBackgroundLoops.
func runIndexDependentTicker(ctx context.Context, repos *repository.Repositories, cfg config.Config, interval time.Duration, name string, fn func() error) {
	runTicker(ctx, interval, func() {
		switch unsafe, err := indexerUnsafe(ctx, repos, cfg.ChainID); {
		case err != nil:
			log.Printf("platform: %s: check indexer safety: %v", name, err)
		case unsafe:
			log.Printf("platform: %s: skipped — indexer reconciliation required", name)
		default:
			runAsReconcilerLeader(ctx, repos, cfg, name, func() {
				if err := fn(); err != nil {
					log.Printf("platform: %s: %v", name, err)
					metrics.ReadModelReconcileErrorsTotal.WithLabelValues(name).Inc()
				}
			})
		}
	})
}

// reconcilerHolderID is this process's identity for the per-reconciler
// leader lease below — it serializes one reconciler leader per
// project/chain in multi-instance mode, generated once per process
// start, mirroring blockchain.NewTxManager's nonce-lease holderID pattern
// (internal/blockchain/txmanager.go).
var reconcilerHolderID = uuid.NewString()

// reconcilerLeaseTTL bounds how long a crashed leader's lease blocks
// another replica from taking over — generous relative to every
// reconciler's 15s tick interval so a merely-slow (not crashed) pass is
// never preempted mid-run by a second replica.
const reconcilerLeaseTTL = 60 * time.Second

// runAsReconcilerLeader acquires a short lease keyed by (name, chainID)
// before running fn, reusing repos.NonceLeases — the same distributed
// fencing primitive blockchain.TxManager already uses for signer-nonce
// coordination — rather than inventing a second lock mechanism. At most
// one replica's tick actually executes a given reconciler at a time; a
// replica whose Acquire fails (another replica currently holds the lease)
// simply skips this tick instead of racing a concurrent
// Upsert/DeleteStaleGeneration pass against the same collection — without
// the lease, multiple replicas amplify the race because their
// delete/reinsert phases can interleave. A single-replica deployment, or
// PERSISTENCE_MODE=memory dev, always acquires immediately since nothing
// else ever holds the key.
func runAsReconcilerLeader(ctx context.Context, repos *repository.Repositories, cfg config.Config, name string, fn func()) {
	key := fmt.Sprintf("reconciler:%s:%d", name, cfg.ChainID)
	token, ok, err := repos.NonceLeases.Acquire(ctx, key, reconcilerHolderID, reconcilerLeaseTTL)
	if err != nil {
		log.Printf("platform: %s: acquire reconciler leader lease: %v", name, err)
		return
	}
	if !ok {
		// Another replica is currently the leader for this reconciler —
		// not an error, just this tick's no-op.
		return
	}
	defer func() {
		relCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := repos.NonceLeases.Release(relCtx, key, reconcilerHolderID, token); err != nil {
			log.Printf("platform: %s: release reconciler leader lease: %v", name, err)
		}
	}()
	fn()
}
