// Command platform loads configuration, connects MongoDB and chain RPC, and serves the
// API. Production requires working MongoDB and a correct-chain RPC; development can
// fall back to memory storage.
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
		// Production startup requires a reachable RPC reporting the configured chain ID
		log.Fatalf("platform: chain RPC unavailable or on the wrong chain; refusing to start in production without a working, correct-chain RPC (see connectChain / CHAIN_ID)")
	}

	// Load deployed addresses and auditor from the active DB project;
	// without one, address-dependent services remain disabled until activation
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

	// Rebuild services after activation or strategy changes without restarting the listener
	if chainClient != nil && app.Project != nil {
		go watchProject(ctx, cfg, repos, chainClient, mongoClient, handler, providers, &bgLoops, addrs)
	}

	// Apply timeouts and header bounds to limit slow-client resource use
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout, ReadTimeout: cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout, IdleTimeout: cfg.HTTPIdleTimeout,
		MaxHeaderBytes: cfg.HTTPMaxHeaderBytes,
	}

	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" {
		// Keep unauthenticated metrics on their own configured listener
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
		// Close all registered key providers at shutdown, including replaced instances
		providers.closeAll()
	}()

	log.Printf("platform: listening on %s (chainId=%d)", cfg.HTTPAddr, cfg.ChainID)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("platform: server error: %v", err)
	}
}

// routerHandler atomically replaces the router when project-dependent services are rebuilt
type routerHandler struct{ p atomic.Pointer[gin.Engine] }

func (h *routerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.p.Load().ServeHTTP(w, r) }
func (h *routerHandler) set(r *gin.Engine)                                { h.p.Store(r) }

// providerRegistry retains created key providers for shutdown cleanup, including replacements
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

// watchProject rebuilds services, router and loops when an Active project's addresses
// change, including activation and strategy swaps. wired holds the running loops'
// address set.
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
			continue // keep watching until an active project is available
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

// loadProjectAddresses returns the active stack or zero addresses until activation
func loadProjectAddresses(ctx context.Context, repos *repository.Repositories) (models.Addresses, string) {
	p, err := repos.Projects.Get(ctx)
	if err != nil || p.Status != models.ProjectStatusActive {
		return models.Addresses{}, ""
	}
	return addressesFromProject(p)
}

// addressesFromProject reads stored addresses/auditor and projected strategy; buildApp
// and reconciliation refresh the auditor from chain state
func addressesFromProject(p *models.Project) (models.Addresses, string) {
	addrs := p.Addresses
	// Watch the projected live strategy instead of the deploy-time address
	if p.Security != nil && p.Security.Strategy != "" {
		addrs.Strategy = p.Security.Strategy
	}
	return addrs, p.Auditor
}

// connectRepositories selects the backend. Production Mongo connect, ping or index
// failures are fatal; development falls back on connect/ping failures but only logs
// index failures.
func connectRepositories(ctx context.Context, cfg config.Config) (*repository.Repositories, *mongodriver.Client) {
	if cfg.PersistenceMode == "memory" {
		log.Printf("platform: PERSISTENCE_MODE=memory - using in-memory repositories (not durable; refused in production)")
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

// fallbackOrFatal rejects production Mongo failure or logs a development memory fallback
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
	// DialChain verifies RPC chain identity before returning a client
	client, err := serverwiring.DialChain(dialCtx, cfg)
	if err != nil {
		log.Printf("platform: chain RPC unavailable (%v)", err)
		return nil
	}
	log.Printf("platform: connected to chain RPC at %s (chainId %d)", cfg.ChainRPCURL, cfg.ChainID)
	return client
}

// loadSigner uses the selected provider (rawHex only in raw mode); missing or invalid
// optional keys return nil for callers to handle
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

// collectSigner registers a non-nil provider and exposes its Signer interface
func collectSigner(providers *[]keys.Provider, signer keys.Provider) keys.Provider {
	if signer != nil {
		*providers = append(*providers, signer)
	}
	return signer
}

// newIPFSReplicationManager adds configured backups; none returns nil, leaving
// development publication on local Kubo
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

// newTxManager uses serverwiring's nonce coordination for request, background and
// opsctl submissions
func newTxManager(cfg config.Config, chainClient blockchain.Client, repos *repository.Repositories) blockchain.TxManager {
	// Use the same lease and fee configuration as opsctl
	return serverwiring.NewTxManager(cfg, chainClient, repos)
}

// buildApp wires services and returns created key providers for shutdown cleanup
func buildApp(cfg config.Config, addrs models.Addresses, auditor string, repos *repository.Repositories, chainClient blockchain.Client, mongoClient *mongodriver.Client) (*api.App, []keys.Provider) {
	var providers []keys.Provider
	app := &api.App{
		Repos:               repos,
		ChainID:             cfg.ChainID,
		ExplorerURL:         cfg.ExplorerURL,
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
	app.MonitoringBlock = cfg.KYCMonitoringAction == "block"
	sanctions := compliance.NewSanctionsList(cfg.SanctionsListFile)
	if _, err := sanctions.Reload(); err != nil {
		// Initial list-load errors stop production; development logs them
		if cfg.Environment == config.EnvProduction {
			log.Fatalf("platform: %v", err)
		}
		log.Printf("platform: %v; no sanctions screening until the file is fixed", err)
	}
	app.Guard = compliance.NewGuard(repos, cfg.MaxAllowedWallets, []string{addrs.Vault, addrs.RedemptionEscrow}, sanctions)

	if chainClient == nil {
		return app, providers // chain-dependent services remain unavailable
	}

	txs := newTxManager(cfg, chainClient, repos)

	if cfg.FactoryAddress != "" {
		// Observe profile-matching wallet deployments; project.Service enforces
		// configured-admin binding and never signs deployments
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
		app.Burns = assets.NewBurnService(app.Records, repos.Burns)
		// Prefer a live auditor read; retain the stored value on failure and retry in reconciliation
		liveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.Records.ReconcileAuditorLive(liveCtx, chainClient); err != nil {
			log.Printf("platform: live SupplyController.auditor() read failed, using %s: %v", auditorAddr, err)
		}
		cancel()
	}

	app.Challenges = compliance.NewChallengeService(repos.WalletChallenges, repos.Investors, cfg.WalletChallengeTTL, "RWA Platform")
	// Share persisted wallet sessions across replicas after ownership verification
	app.Sessions = auth.NewSessionManager(repos.WalletSessions, cfg.WalletSessionTTL)
	// Share admin login challenges across replicas, using WalletChallengeTTL
	app.AdminChallenges = auth.NewAdminChallengeService(repos.AdminChallenges, cfg.WalletChallengeTTL, "RWA Platform")
	// Configured KYC adapters authenticate deliveries; WebhookService handles replay,
	// freshness, ownership and decision persistence
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
		// If provider construction still fails, log it and disable the KYC endpoints
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

	// Check Mongo health when a persistent backend is in use
	app.ReadyCheck = func() error {
		// Recheck RPC chain identity when readiness is queried
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
		// Report not_ready while the indexer requires reconciliation
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

// startBackgroundLoops runs indexing, transaction refresh and read-model reconciliation
// until its context is cancelled
func startBackgroundLoops(ctx context.Context, cfg config.Config, addrs models.Addresses, repos *repository.Repositories, chainClient blockchain.Client, app *api.App, mongoClient *mongodriver.Client) {
	// Reconcile deployment now and on ticks, adopting verified events or demoting projects
	// without authorized backing events
	if app.Project != nil {
		reconcileDeploy := func() {
			if err := app.Project.ReconcileDeployment(ctx, repos.ChainEvents, repos.AssetProfiles); err != nil {
				log.Printf("platform: project deployment reconcile error: %v", err)
			}
		}
		reconcileDeploy()
		go runTicker(ctx, 10*time.Second, reconcileDeploy)
	}

	// Watch the configured factory and the active project stack
	addresses := serverwiring.IndexerAddresses(addrs, cfg.FactoryAddress)
	if len(addresses) > 0 {
		// Use stored hashes to walk back to a common ancestor during reorg recovery
		idx := indexer.New(chainClient, repos.IndexerCheckpoints, repos.ChainEvents, cfg.ChainID, addresses, cfg.StartBlock, serverwiring.BuildDecoder(addrs, cfg.FactoryAddress),
			indexer.WithBlockHashRepository(repos.IndexerBlockHashes),
			// Atomically commit events, hashes, pruning and the versioned checkpoint
			indexer.WithChunkCommitter(repos.IndexerChunks),
			// Persist undecodable logs for operator inspection and retry
			indexer.WithDeadLetterQueue(repos.IndexerDeadLetters))
		go runTicker(ctx, 5*time.Second, func() {
			// Cancellation is expected during shutdown or service rewiring
			if err := idx.Poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("platform: indexer poll error: %v", err)
			}
		})
	}

	if addrs.SupplyController != "" || addrs.Compliance != "" || addrs.Vault != "" {
		txs := newTxManager(cfg, chainClient, repos)
		// Refresh pending transaction state immediately after restart, before periodic polling
		if err := txs.RefreshStatuses(ctx, cfg.Confirmations); err != nil {
			log.Printf("platform: tx status refresh error: %v", err)
		}
		go runTicker(ctx, 10*time.Second, func() {
			if err := txs.RefreshStatuses(ctx, cfg.Confirmations); err != nil {
				log.Printf("platform: tx status refresh error: %v", err)
			}
		})
	}

	// Verify retrieval and digest at configured publication destinations
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

	// Gate dependent work while the indexer requires reconciliation
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
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "assets burned-reconcile", func() error {
			return app.Burns.ReconcileBurned(ctx, repos.ChainEvents, cfg.ChainID, addrs.SupplyController)
		})
		// Refresh auditor rotations without restarting
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "assets auditor-reconcile", func() error {
			return app.Records.ReconcileAuditor(ctx, repos.ChainEvents, cfg.ChainID, addrs.SupplyController)
		})
	}

	if addrs.Compliance != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "compliance reconcile", func() error {
			return compliance.Reconcile(ctx, repos.ChainEvents, cfg.ChainID, addrs.Compliance, repos.Investors)
		})
	}

	// Project security state follows indexed events and the shared indexer-safety gate
	if addrs.Token != "" || addrs.Strategy != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "project security-reconcile", func() error {
			return project.ReconcileSecurity(ctx, repos.Projects, repos.ChainEvents, repos.IndexerCheckpoints, cfg.ChainID)
		})
	}

	// Add indexed wallet transactions to the history alongside server submissions
	if addrs.Token != "" || addrs.Vault != "" || addrs.RedemptionEscrow != "" ||
		addrs.SupplyController != "" || addrs.Compliance != "" || addrs.Strategy != "" {
		go runIndexDependentTicker(ctx, repos, cfg, 15*time.Second, "stack transactions-reconcile", func() error {
			return txindex.ReconcileStackTransactions(ctx, repos.Transactions, repos.ChainEvents, repos.IndexerCheckpoints, cfg.ChainID)
		})
	}

	// Relay queued webhook decisions now and on ticks; Accepted decisions need app.Status
	if app.Webhooks != nil {
		webhookReconciler := compliance.NewWebhookReconciler(repos.KYCEvents, repos.Transactions, app.Status)
		webhookReconciler.SetGuard(app.Guard, func(ctx context.Context, kind, address, detail string) {
			app.RaiseAlert(ctx, kind, "kyc-reconciler", address, detail)
		})
		if err := webhookReconciler.Reconcile(ctx); err != nil {
			log.Printf("platform: webhook reconcile error: %v", err)
		}
		go runTicker(ctx, 10*time.Second, func() {
			if err := webhookReconciler.Reconcile(ctx); err != nil {
				log.Printf("platform: webhook reconcile error: %v", err)
			}
		})
	}

	// Check wiring after adoption without changing the project's deployment status
	if app.Project != nil && addrs.Token != "" {
		go runTicker(ctx, 5*time.Minute, func() { checkConfigDrift(ctx, repos, chainClient, app) })
	}

	// Screen Allowed holders at startup and after successful list reloads
	reportSanctionedHolders(ctx, app)
	go runTicker(ctx, 30*time.Second, func() { rescreenOnSanctionsReload(ctx, app) })

	go runTicker(ctx, 30*time.Second, func() { refreshBusinessGauges(ctx, repos, app) })
	go runTicker(ctx, 5*time.Minute, func() { evaluateAlerts(ctx, cfg, repos, app) })

	if mongoClient != nil {
		go runTicker(ctx, 30*time.Second, func() { checkStorageHealth(ctx, mongoClient, app) })
	}
}

// checkStorageHealth updates the storage gauge and logs, counts and attempts to audit failures
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

// refreshBusinessGauges updates metrics from repositories and chain reads;
// a failed read retains the previous value
func refreshBusinessGauges(ctx context.Context, repos *repository.Repositories, app *api.App) {
	if allowed, err := app.Guard.AllowedWallets(ctx, time.Now()); err == nil {
		metrics.AllowedWallets.Set(float64(len(allowed)))
	}
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

// evaluateAlerts logs and counts redemption findings and attempts an audit append
func evaluateAlerts(ctx context.Context, cfg config.Config, repos *repository.Repositories, app *api.App) {
	// Another COMPLIANCE_ROLE holder can allow wallets past the cap directly
	if limit := app.Guard.Cap(); limit > 0 {
		if allowed, err := app.Guard.AllowedWallets(ctx, time.Now()); err != nil {
			log.Printf("platform: alert evaluation: listing allowed wallets: %v", err)
		} else if len(allowed) > limit {
			app.RaiseAlert(ctx, compliance.AlertHolderCapExceeded, "system", "",
				fmt.Sprintf("%d wallets are Allowed, above the cap of %d", len(allowed), limit))
		}
	}

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

// rescreenOnSanctionsReload reports listed Allowed holders after a successful file reload
func rescreenOnSanctionsReload(ctx context.Context, app *api.App) {
	changed, err := app.Guard.ReloadSanctions()
	if err != nil {
		log.Printf("platform: %v", err)
	}
	if changed {
		reportSanctionedHolders(ctx, app)
	}
}

// reportSanctionedHolders alerts on listed, unexpired Allowed wallets, excluding system addresses
func reportSanctionedHolders(ctx context.Context, app *api.App) {
	listed, err := app.Guard.SanctionedAllowed(ctx, time.Now())
	if err != nil {
		log.Printf("platform: sanctions screening: listing allowed wallets: %v", err)
		return
	}
	for _, a := range listed {
		app.RaiseAlert(ctx, compliance.AlertSanctionsMatch, "system", a, "Allowed wallet is on the sanctions list; review and block it if confirmed")
	}
}

// checkConfigDrift logs and counts wiring differences, attempts audit writes and
// updates the gauge; read failures retain its value
func checkConfigDrift(ctx context.Context, repos *repository.Repositories, chainClient blockchain.Client, app *api.App) {
	ok, reason, err := project.CheckConfigDrift(ctx, chainClient, repos.Projects)
	if err != nil {
		// Leave the previous drift result intact when an RPC read fails
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

// redemptionTimeoutSeconds uses the deployed timeout, falling back to 14 days if
// missing or zero
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

// indexerUnsafe reads ReconciliationRequired; a missing startup checkpoint is not unsafe
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

// runIndexDependentTicker gates work on indexer safety and logs/counts callback
// failures
func runIndexDependentTicker(ctx context.Context, repos *repository.Repositories, cfg config.Config, interval time.Duration, name string, fn func() error) {
	runTicker(ctx, interval, func() {
		switch unsafe, err := indexerUnsafe(ctx, repos, cfg.ChainID); {
		case err != nil:
			log.Printf("platform: %s: check indexer safety: %v", name, err)
		case unsafe:
			log.Printf("platform: %s: skipped - indexer reconciliation required", name)
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

// reconcilerHolderID identifies this process in shared reconciler leases
var reconcilerHolderID = uuid.NewString()

// reconcilerLeaseTTL controls takeover when a leader stops releasing its lease
const reconcilerLeaseTTL = 60 * time.Second

// runAsReconcilerLeader acquires a name/chain lease before fn; it must finish before
// expiry because the lease is not renewed
func runAsReconcilerLeader(ctx context.Context, repos *repository.Repositories, cfg config.Config, name string, fn func()) {
	key := fmt.Sprintf("reconciler:%s:%d", name, cfg.ChainID)
	token, ok, err := repos.NonceLeases.Acquire(ctx, key, reconcilerHolderID, reconcilerLeaseTTL)
	if err != nil {
		log.Printf("platform: %s: acquire reconciler leader lease: %v", name, err)
		return
	}
	if !ok {
		// Skip the tick when the lease is held elsewhere
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
