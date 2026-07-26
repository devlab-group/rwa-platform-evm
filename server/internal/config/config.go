// Package config loads server configuration from a single YAML file whose
// path is passed via --config (see LoadFile). ALL configuration — including
// secrets (the compliance hot key, the KYC webhook HMAC secret, the admin JWT
// secret, Mongo URI, chain RPC, factory address) — lives
// in that file; the operator is responsible for its filesystem permissions,
// exactly as with a .env or supervisor config (secrets-in-file is an
// accepted deployment posture — see server/config.example.yaml).
//
// The YAML is decoded into a flat key/value view (fileSchema.toEnvMap) that
// the pure, I/O-free validation engine (load) then resolves and validates —
// so every default and every production fail-closed check is applied
// identically regardless of how the raw values arrived. That engine is also
// exposed as LoadFromMap for table-driven tests.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Environment selects the deployment-safety policy Load enforces.
// "production" fails startup on configuration that would otherwise silently
// degrade a security or durability guarantee (empty/weak webhook secret, no
// reachable Mongo); "development" keeps the permissive local/dev-friendly
// defaults.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// MinWebhookSecretBytes is the documented minimum length for
// KYC_WEBHOOK_HMAC_SECRET. It's a length floor on the configured string,
// not a true
// entropy measurement — good enough to reject trivial/placeholder secrets
// ("changeme", "secret", ...) without requiring an entropy estimator.
const MinWebhookSecretBytes = 32

// Config is the fully resolved server configuration.
type Config struct {
	// Environment gates production-only startup checks (see Environment's
	// doc comment). Defaults to "development".
	Environment Environment

	// HTTP
	HTTPAddr string

	// Chain
	ChainRPCURL   string
	ChainID       int64
	Confirmations uint64
	FeeMode       string // "eip1559" or "legacy"

	// Contract bootstrap. Config is BOOTSTRAP-ONLY for the contract stack: it
	// carries ONLY the factory address (the one-time deploy entry point) and
	// the block the indexer begins scanning from. Every DEPLOYED contract
	// address (token/compliance/supply-controller/vault/redemption-escrow/
	// strategy/quote-token) and the auditor are read from the DB Project
	// record (models.Project), kept live by the indexer projector — NOT from
	// config, so a redeploy never requires editing this file. FactoryAddress
	// may be empty for a server that only serves an already-deployed project
	// (addresses then come purely from the DB record).
	FactoryAddress string
	// StartBlock is the block the indexer begins scanning from when no
	// checkpoint exists yet — i.e. the factory deploy block. 0 (the default)
	// scans from genesis, which is correct for a fresh local chain but
	// wasteful against a long-lived network where the factory was deployed at
	// a known height.
	StartBlock uint64

	// ProjectID is the UUID that identifies this deployment's single Asset
	// Profile. It is set by the operator in config BEFORE the platform runs
	// and acts as a gate: the server rejects any profile whose projectId does
	// not match, and the admin web reads it from GET /api/v1/config rather
	// than minting a fresh one, so the on-chain projectId (keccak256 of this
	// UUID) is fixed by whoever provisions the server. Empty leaves the gate
	// off (development/test); EnvProduction requires it.
	ProjectID string

	// Mongo
	MongoURI string
	MongoDB  string

	// PersistenceMode is "mongo" (default) or "memory". In-memory
	// repositories are only permitted behind this development/test flag:
	// "memory" is an explicit opt-in for local smoke-testing/CI without
	// docker-compose, and is refused in EnvProduction. It does NOT control
	// the fallback behavior when a Mongo connection fails while
	// PersistenceMode=="mongo" — see cmd/platform's connectRepositories,
	// which fails startup on that instead of falling back, in EnvProduction.
	PersistenceMode string

	// IPFS (Kubo HTTP API)
	IPFSAPIURL string

	// IPFS replication: a published asset
	// package is not treated as durably published (see
	// ipfs.ReplicationManager) until at least IPFSReplicationThreshold of
	// the configured backup destinations below have pinned it. Empty
	// destinations means no replication is configured — the platform then
	// falls back to a single local-Kubo-node ipfsPinner exactly as before
	// this existed (a documented, opt-in-to-harden gap, not a silent one:
	// see docs/operator/operator-guide.md).
	//
	// IPFSBackupArchiveDir, if set, adds a local-filesystem reproducible
	// content archive (ipfs.FileArchiveClient) as one backup destination —
	// the option that needs no external commercial service.
	IPFSBackupArchiveDir string
	// IPFSBackupKuboURL, if set, adds a second, independent Kubo node as
	// another backup destination.
	IPFSBackupKuboURL string
	// IPFSReplicationThreshold is how many configured backup destinations
	// (not counting the primary local node) must succeed before a package
	// counts as durably published. Meaningless (and ignored) if no backup
	// destination is configured at all.
	IPFSReplicationThreshold int

	// Security
	KYCWebhookHMACSecret string
	IdempotencyTTL       time.Duration
	WalletChallengeTTL   time.Duration
	// WalletSessionTTL bounds the lifetime of a subject-scoped investor
	// session minted at challenge-verify. Short by design: a
	// lapsed session costs the investor one wallet signature to re-prove
	// ownership, not a lost credential. Mirrors WalletChallengeTTL's knob.
	WalletSessionTTL time.Duration
	// JWTTTL bounds the lifetime of the admin JWT issued at POST /auth/session
	// after a successful wallet-signature challenge. Short-by-design: a lapsed
	// token costs the admin one re-login (a wallet signature), not a lost
	// credential. Replaces the former operator-session TTL — admin auth is now
	// a stateless HMAC JWT (see internal/auth.IssueAdminJWT), not a
	// server-stored operator session.
	JWTTTL time.Duration

	// ComplianceKeyHex is the ONLY server hot key (hex-encoded private key),
	// used only in KeyProviderMode "raw" (the default — see internal/keys). An
	// empty string means "not configured"; the compliance status-write feature
	// is then disabled rather than erroring at startup. Deployment and the
	// auditor-signed mint moved to the admin's wallet, so the former relayer
	// and pricer keys no longer exist.
	ComplianceKeyHex string

	// KeyProviderMode selects internal/keys' backend for every hot-key role
	// uniformly: "raw" (default, the *KeyHex fields above), "local-keystore",
	// "vault", or "kms-mock". See internal/keys' package doc for what each
	// mode needs from the fields below.
	KeyProviderMode      string
	KeystoreDir          string
	KeystorePasswordFile string
	VaultAddr            string
	VaultToken           string
	VaultKeyPrefix       string
	KMSMockSeedHex       string

	// MaxRequestBodyBytes bounds every request body. 0 disables the limit.
	MaxRequestBodyBytes int64

	// RateLimitRPS/RateLimitBurst configure the per-client-IP token bucket
	// (internal/auth.RateLimit). RateLimitRPS<=0 disables rate limiting.
	RateLimitRPS   float64
	RateLimitBurst int

	// TrustedProxies is passed to gin.Engine.SetTrustedProxies (comma-
	// separated IPs/CIDRs). Empty (the default) trusts NO proxies: Gin's
	// c.ClientIP() — which the rate limiter keys on — then uses the raw
	// TCP RemoteAddr only, ignoring X-Forwarded-For entirely. That's the
	// safe default for a directly internet-facing deployment: trusting
	// every proxy (gin's own default before SetTrustedProxies is called
	// explicitly) lets any caller spoof X-Forwarded-For to bypass
	// per-IP rate limiting outright. Set this to the real reverse
	// proxy/load balancer's address when running behind one.
	TrustedProxies []string

	// MetricsAddr, if non-empty, serves Prometheus /metrics on its own
	// listener (deliberately not the public API port — see cmd/platform).
	// Defaults to loopback-only so /metrics is not exposed on all interfaces
	// without auth: /metrics has no authentication
	// of its own, so the safe-by-default posture is a reverse proxy or
	// scraper running on the same host/network namespace. Set this to a
	// non-loopback address explicitly (and put it behind your own
	// auth/firewall) to scrape remotely.
	MetricsAddr string

	// HTTP hardening: read/write/idle timeouts and a header-size cap guard
	// both listeners against slowloris-style slow-client connection
	// exhaustion. Applied to both
	// the public API listener and the metrics listener.
	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPMaxHeaderBytes    int

	// Alert evaluator thresholds for the pending-redemption SLA and
	// funded-claim-failure evaluators — see internal/alerts.
	PendingRedemptionSLA  time.Duration
	FundedClaimFailureSLA time.Duration

	// MaxFeePerGasWei / MaxTipPerGasWei / MaxTxTotalCostWeiStr are absolute
	// gas-price/fee-cap/tip-cap/total-cost limits the transaction manager
	// will ever sign for, as base-10 wei strings (unbounded precision,
	// unlike int64, matters at wei scale). Empty (the default) means "no cap"
	// for that dimension — appropriate for a local/dev chain; operators on a
	// real network should set these.
	MaxFeePerGasWei   string
	MaxTipPerGasWei   string
	MaxTxTotalCostWei string

	// TxCoordinationMode selects how the transaction manager serializes a
	// signer's nonce sequence: "in-process"
	// (default) — an in-process mutex only, safe for exactly one server
	// process per configured hot key (see operator-guide.md §7a) — or
	// "mongo-lease" — additionally acquires a distributed lease (with a
	// strictly-monotonic fencing token) from MongoDB before managing a
	// signer's nonces, safe for multiple replicas sharing the same hot key.
	// "in-process" is the single-process default; opting into "mongo-lease"
	// requires PersistenceMode=="mongo" (a lease with no durable backing
	// store is not a lease).
	TxCoordinationMode string
	// TxLeaseTTL bounds how long a "mongo-lease" holder's lease survives
	// without renewal before another replica may take over — long enough
	// to cover one Submit/Replace call's sign+persist+broadcast latency,
	// short enough that a crashed holder's hot key isn't unusable for long.
	TxLeaseTTL time.Duration

	// AdminAddress is the single admin wallet (the contract deployer / live
	// DEFAULT_ADMIN_ROLE holder). Admin auth is wallet-signature based: POST
	// /auth/challenge issues a single-use message, POST /auth/session recovers
	// the personal_sign signer and — if it equals this address (case-
	// insensitive / checksum-normalized) — issues an admin JWT. Empty means no
	// admin is configured and no admin JWT can ever be issued; production
	// requires a valid 0x-address here.
	AdminAddress string
	// JWTSecret is the HMAC-SHA256 signing key for admin JWTs (internal/auth).
	// Empty means admin JWTs cannot be issued/verified; production requires at
	// least MinProductionSecretBytes, same floor as the other secrets.
	JWTSecret string
}

// envLookup abstracts os.Getenv so tests can supply a fake environment
// without mutating process-global state.
type envLookup func(key string) (string, bool)

// LoadFile reads and parses the YAML configuration at path, then resolves
// defaults and runs every validation (including the production fail-closed
// checks) via the shared engine. Unknown YAML keys are rejected so a typo in
// a security-relevant key can never silently fall back to a default.
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %q: %w", path, err)
	}
	var f fileSchema
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty document (Decode returns io.EOF) is valid: it means "use every
	// default", so fall through with a zero fileSchema.
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: parsing %q: %w", path, err)
	}
	return LoadFromMap(f.toEnvMap())
}

// LoadFromMap builds a Config from an explicit key/value map. It is the pure,
// I/O-free validation engine shared by LoadFile (which flattens the parsed
// YAML into such a map) and by the table-driven config tests.
func LoadFromMap(env map[string]string) (Config, error) {
	return load(func(key string) (string, bool) { v, ok := env[key]; return v, ok })
}

// fileSchema is the on-disk YAML shape. It is decoded and then flattened into
// the flat key/value view LoadFromMap consumes (toEnvMap), so the YAML can be
// grouped and human-friendly while the validation engine stays a single flat
// lookup. Scalars whose zero value is a meaningful configuration (numbers,
// booleans) use pointers so "omitted" is distinguishable from "explicitly 0/
// false" — an omitted numeric key must fall through to its documented
// default, while an explicit 0 must be honored (and, in production, rejected
// where a zero disables a control). Empty strings are treated as omitted
// (matching getString), so absent string keys also take their defaults.
type fileSchema struct {
	Environment string `yaml:"environment"`

	HTTP struct {
		Addr                string   `yaml:"addr"`
		MetricsAddr         string   `yaml:"metrics_addr"`
		ReadHeaderTimeout   string   `yaml:"read_header_timeout"`
		ReadTimeout         string   `yaml:"read_timeout"`
		WriteTimeout        string   `yaml:"write_timeout"`
		IdleTimeout         string   `yaml:"idle_timeout"`
		MaxHeaderBytes      *int64   `yaml:"max_header_bytes"`
		MaxRequestBodyBytes *int64   `yaml:"max_request_body_bytes"`
		TrustedProxies      []string `yaml:"trusted_proxies"`
	} `yaml:"http"`

	Chain struct {
		RPCURL            string  `yaml:"rpc_url"`
		ID                *int64  `yaml:"id"`
		Confirmations     *uint64 `yaml:"confirmations"`
		FeeMode           string  `yaml:"fee_mode"`
		MaxFeePerGasWei   string  `yaml:"max_fee_per_gas_wei"`
		MaxTipPerGasWei   string  `yaml:"max_tip_per_gas_wei"`
		MaxTxTotalCostWei string  `yaml:"max_tx_total_cost_wei"`
	} `yaml:"chain"`

	// Contract is BOOTSTRAP-ONLY (see Config.FactoryAddress): the factory
	// deploy entry point, the indexer's start block, and the profile's
	// projectId. Deployed addresses + auditor live in the DB Project record,
	// not here.
	Contract struct {
		FactoryAddress string  `yaml:"factory_address"`
		StartBlock     *uint64 `yaml:"start_block"`
		ProjectID      string  `yaml:"project_id"`
	} `yaml:"contract"`

	Mongo struct {
		URI             string `yaml:"uri"`
		DB              string `yaml:"db"`
		PersistenceMode string `yaml:"persistence_mode"`
	} `yaml:"mongo"`

	IPFS struct {
		APIURL               string `yaml:"api_url"`
		BackupArchiveDir     string `yaml:"backup_archive_dir"`
		BackupKuboURL        string `yaml:"backup_kubo_url"`
		ReplicationThreshold *int64 `yaml:"replication_threshold"`
	} `yaml:"ipfs"`

	Security struct {
		KYCWebhookHMACSecret string   `yaml:"kyc_webhook_hmac_secret"`
		IdempotencyTTL       string   `yaml:"idempotency_ttl"`
		WalletChallengeTTL   string   `yaml:"wallet_challenge_ttl"`
		WalletSessionTTL     string   `yaml:"wallet_session_ttl"`
		JWTTTL               string   `yaml:"jwt_ttl"`
		AdminAddress         string   `yaml:"admin_address"`
		JWTSecret            string   `yaml:"jwt_secret"`
		RateLimitRPS         *float64 `yaml:"rate_limit_rps"`
		RateLimitBurst       *int64   `yaml:"rate_limit_burst"`
	} `yaml:"security"`

	Keys struct {
		ProviderMode         string `yaml:"provider_mode"`
		ComplianceKey        string `yaml:"compliance_key"`
		KeystoreDir          string `yaml:"keystore_dir"`
		KeystorePasswordFile string `yaml:"keystore_password_file"`
		VaultAddr            string `yaml:"vault_addr"`
		VaultToken           string `yaml:"vault_token"`
		VaultKeyPrefix       string `yaml:"vault_key_prefix"`
		KMSMockSeed          string `yaml:"kms_mock_seed"`
	} `yaml:"keys"`

	Tx struct {
		CoordinationMode string `yaml:"coordination_mode"`
		LeaseTTL         string `yaml:"lease_ttl"`
	} `yaml:"tx"`

	Alerts struct {
		PendingRedemptionSLA  string `yaml:"pending_redemption_sla"`
		FundedClaimFailureSLA string `yaml:"funded_claim_failure_sla"`
	} `yaml:"alerts"`

	// ProductionOverrides map to the narrowly-named PRODUCTION_ALLOW_*
	// emergency escape hatches. Absent/false means "not overridden"; only an
	// explicit true opts out of a fail-closed check.
	ProductionOverrides struct {
		AllowUnboundedRequestBody *bool `yaml:"allow_unbounded_request_body"`
		AllowDisabledRateLimit    *bool `yaml:"allow_disabled_rate_limit"`
		AllowUnboundedFees        *bool `yaml:"allow_unbounded_fees"`
	} `yaml:"production_overrides"`
}

// toEnvMap flattens the parsed YAML into the flat key/value view the
// validation engine (load) consumes. It emits a key only when the operator
// actually supplied a value, so every omitted key falls through to its
// documented default inside load — the one exception being explicit numeric/
// boolean zero values, which are carried through (via the pointer fields) so
// they can be honored and, in production, rejected where a zero disables a
// control.
func (f fileSchema) toEnvMap() map[string]string {
	m := map[string]string{}
	setStr := func(key, v string) {
		if strings.TrimSpace(v) != "" {
			m[key] = v
		}
	}
	setI64 := func(key string, p *int64) {
		if p != nil {
			m[key] = strconv.FormatInt(*p, 10)
		}
	}
	setU64 := func(key string, p *uint64) {
		if p != nil {
			m[key] = strconv.FormatUint(*p, 10)
		}
	}
	setF64 := func(key string, p *float64) {
		if p != nil {
			m[key] = strconv.FormatFloat(*p, 'f', -1, 64)
		}
	}
	setBool := func(key string, p *bool) {
		if p != nil && *p {
			m[key] = "true"
		}
	}

	setStr("ENVIRONMENT", f.Environment)

	setStr("HTTP_ADDR", f.HTTP.Addr)
	setStr("METRICS_ADDR", f.HTTP.MetricsAddr)
	setStr("HTTP_READ_HEADER_TIMEOUT", f.HTTP.ReadHeaderTimeout)
	setStr("HTTP_READ_TIMEOUT", f.HTTP.ReadTimeout)
	setStr("HTTP_WRITE_TIMEOUT", f.HTTP.WriteTimeout)
	setStr("HTTP_IDLE_TIMEOUT", f.HTTP.IdleTimeout)
	setI64("HTTP_MAX_HEADER_BYTES", f.HTTP.MaxHeaderBytes)
	setI64("MAX_REQUEST_BODY_BYTES", f.HTTP.MaxRequestBodyBytes)
	if len(f.HTTP.TrustedProxies) > 0 {
		setStr("TRUSTED_PROXIES", strings.Join(f.HTTP.TrustedProxies, ","))
	}

	setStr("CHAIN_RPC_URL", f.Chain.RPCURL)
	setI64("CHAIN_ID", f.Chain.ID)
	setU64("CHAIN_CONFIRMATIONS", f.Chain.Confirmations)
	setStr("CHAIN_FEE_MODE", f.Chain.FeeMode)
	setStr("TX_MAX_FEE_PER_GAS_WEI", f.Chain.MaxFeePerGasWei)
	setStr("TX_MAX_TIP_PER_GAS_WEI", f.Chain.MaxTipPerGasWei)
	setStr("TX_MAX_TOTAL_COST_WEI", f.Chain.MaxTxTotalCostWei)

	setStr("FACTORY_ADDRESS", f.Contract.FactoryAddress)
	setU64("CONTRACT_START_BLOCK", f.Contract.StartBlock)
	setStr("PROJECT_ID", f.Contract.ProjectID)

	setStr("MONGO_URI", f.Mongo.URI)
	setStr("MONGO_DB", f.Mongo.DB)
	setStr("PERSISTENCE_MODE", f.Mongo.PersistenceMode)

	setStr("IPFS_API_URL", f.IPFS.APIURL)
	setStr("IPFS_BACKUP_ARCHIVE_DIR", f.IPFS.BackupArchiveDir)
	setStr("IPFS_BACKUP_KUBO_URL", f.IPFS.BackupKuboURL)
	setI64("IPFS_REPLICATION_THRESHOLD", f.IPFS.ReplicationThreshold)

	setStr("KYC_WEBHOOK_HMAC_SECRET", f.Security.KYCWebhookHMACSecret)
	setStr("IDEMPOTENCY_TTL", f.Security.IdempotencyTTL)
	setStr("WALLET_CHALLENGE_TTL", f.Security.WalletChallengeTTL)
	setStr("WALLET_SESSION_TTL", f.Security.WalletSessionTTL)
	setStr("JWT_TTL", f.Security.JWTTTL)
	setStr("ADMIN_ADDRESS", f.Security.AdminAddress)
	setStr("JWT_SECRET", f.Security.JWTSecret)
	setF64("RATE_LIMIT_RPS", f.Security.RateLimitRPS)
	setI64("RATE_LIMIT_BURST", f.Security.RateLimitBurst)

	setStr("KEY_PROVIDER_MODE", f.Keys.ProviderMode)
	setStr("COMPLIANCE_KEY", f.Keys.ComplianceKey)
	setStr("KEYSTORE_DIR", f.Keys.KeystoreDir)
	setStr("KEYSTORE_PASSWORD_FILE", f.Keys.KeystorePasswordFile)
	setStr("VAULT_ADDR", f.Keys.VaultAddr)
	setStr("VAULT_TOKEN", f.Keys.VaultToken)
	setStr("VAULT_KEY_PREFIX", f.Keys.VaultKeyPrefix)
	setStr("KMS_MOCK_SEED", f.Keys.KMSMockSeed)

	setStr("TX_COORDINATION_MODE", f.Tx.CoordinationMode)
	setStr("TX_LEASE_TTL", f.Tx.LeaseTTL)

	setStr("PENDING_REDEMPTION_SLA", f.Alerts.PendingRedemptionSLA)
	setStr("FUNDED_CLAIM_FAILURE_SLA", f.Alerts.FundedClaimFailureSLA)

	setBool("PRODUCTION_ALLOW_UNBOUNDED_REQUEST_BODY", f.ProductionOverrides.AllowUnboundedRequestBody)
	setBool("PRODUCTION_ALLOW_DISABLED_RATE_LIMIT", f.ProductionOverrides.AllowDisabledRateLimit)
	setBool("PRODUCTION_ALLOW_UNBOUNDED_FEES", f.ProductionOverrides.AllowUnboundedFees)

	return m
}

func load(lookup envLookup) (Config, error) {
	cfg := Config{
		Environment:          Environment(getString(lookup, "ENVIRONMENT", string(EnvDevelopment))),
		HTTPAddr:             getString(lookup, "HTTP_ADDR", ":8080"),
		ChainRPCURL:          getString(lookup, "CHAIN_RPC_URL", "http://127.0.0.1:8545"),
		FeeMode:              getString(lookup, "CHAIN_FEE_MODE", "eip1559"),
		MongoURI:             getString(lookup, "MONGO_URI", "mongodb://127.0.0.1:27017"),
		MongoDB:              getString(lookup, "MONGO_DB", "rwa_platform"),
		PersistenceMode:      getString(lookup, "PERSISTENCE_MODE", "mongo"),
		IPFSAPIURL:           getString(lookup, "IPFS_API_URL", "http://127.0.0.1:5001"),
		IPFSBackupArchiveDir: getString(lookup, "IPFS_BACKUP_ARCHIVE_DIR", ""),
		IPFSBackupKuboURL:    getString(lookup, "IPFS_BACKUP_KUBO_URL", ""),
		KYCWebhookHMACSecret: getString(lookup, "KYC_WEBHOOK_HMAC_SECRET", ""),

		FactoryAddress: getString(lookup, "FACTORY_ADDRESS", ""),
		ProjectID:      getString(lookup, "PROJECT_ID", ""),

		ComplianceKeyHex: getString(lookup, "COMPLIANCE_KEY", ""),

		KeyProviderMode:      getString(lookup, "KEY_PROVIDER_MODE", "raw"),
		KeystoreDir:          getString(lookup, "KEYSTORE_DIR", ""),
		KeystorePasswordFile: getString(lookup, "KEYSTORE_PASSWORD_FILE", ""),
		VaultAddr:            getString(lookup, "VAULT_ADDR", ""),
		VaultToken:           getString(lookup, "VAULT_TOKEN", ""),
		VaultKeyPrefix:       getString(lookup, "VAULT_KEY_PREFIX", "rwa-"),
		KMSMockSeedHex:       getString(lookup, "KMS_MOCK_SEED", ""),

		MetricsAddr: getString(lookup, "METRICS_ADDR", "127.0.0.1:9090"),

		MaxFeePerGasWei:   getString(lookup, "TX_MAX_FEE_PER_GAS_WEI", ""),
		MaxTipPerGasWei:   getString(lookup, "TX_MAX_TIP_PER_GAS_WEI", ""),
		MaxTxTotalCostWei: getString(lookup, "TX_MAX_TOTAL_COST_WEI", ""),

		TxCoordinationMode: getString(lookup, "TX_COORDINATION_MODE", "in-process"),

		AdminAddress: getString(lookup, "ADMIN_ADDRESS", ""),
		JWTSecret:    getString(lookup, "JWT_SECRET", ""),
	}

	chainID, err := getInt64(lookup, "CHAIN_ID", 31337)
	if err != nil {
		return Config{}, err
	}
	cfg.ChainID = chainID

	confirmations, err := getUint64(lookup, "CHAIN_CONFIRMATIONS", 3)
	if err != nil {
		return Config{}, err
	}
	cfg.Confirmations = confirmations

	startBlock, err := getUint64(lookup, "CONTRACT_START_BLOCK", 0)
	if err != nil {
		return Config{}, err
	}
	cfg.StartBlock = startBlock

	idempotencyTTL, err := getDuration(lookup, "IDEMPOTENCY_TTL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg.IdempotencyTTL = idempotencyTTL

	challengeTTL, err := getDuration(lookup, "WALLET_CHALLENGE_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.WalletChallengeTTL = challengeTTL

	sessionTTL, err := getDuration(lookup, "WALLET_SESSION_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.WalletSessionTTL = sessionTTL

	jwtTTL, err := getDuration(lookup, "JWT_TTL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg.JWTTTL = jwtTTL

	maxBody, err := getInt64(lookup, "MAX_REQUEST_BODY_BYTES", 2<<20) // 2 MiB
	if err != nil {
		return Config{}, err
	}
	cfg.MaxRequestBodyBytes = maxBody

	rateLimitRPS, err := getFloat64(lookup, "RATE_LIMIT_RPS", 50)
	if err != nil {
		return Config{}, err
	}
	cfg.RateLimitRPS = rateLimitRPS

	rateLimitBurst, err := getInt64(lookup, "RATE_LIMIT_BURST", 100)
	if err != nil {
		return Config{}, err
	}
	cfg.RateLimitBurst = int(rateLimitBurst)

	ipfsReplicationThreshold, err := getInt64(lookup, "IPFS_REPLICATION_THRESHOLD", 1)
	if err != nil {
		return Config{}, err
	}
	cfg.IPFSReplicationThreshold = int(ipfsReplicationThreshold)

	if v, ok := lookup("TRUSTED_PROXIES"); ok && strings.TrimSpace(v) != "" {
		var proxies []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				proxies = append(proxies, p)
			}
		}
		cfg.TrustedProxies = proxies
	}

	pendingSLA, err := getDuration(lookup, "PENDING_REDEMPTION_SLA", 48*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg.PendingRedemptionSLA = pendingSLA

	fundedSLA, err := getDuration(lookup, "FUNDED_CLAIM_FAILURE_SLA", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg.FundedClaimFailureSLA = fundedSLA

	if cfg.HTTPReadHeaderTimeout, err = getDuration(lookup, "HTTP_READ_HEADER_TIMEOUT", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPReadTimeout, err = getDuration(lookup, "HTTP_READ_TIMEOUT", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPWriteTimeout, err = getDuration(lookup, "HTTP_WRITE_TIMEOUT", 60*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.HTTPIdleTimeout, err = getDuration(lookup, "HTTP_IDLE_TIMEOUT", 120*time.Second); err != nil {
		return Config{}, err
	}
	maxHeaderBytes, err := getInt64(lookup, "HTTP_MAX_HEADER_BYTES", 32<<10) // 32 KiB
	if err != nil {
		return Config{}, err
	}
	cfg.HTTPMaxHeaderBytes = int(maxHeaderBytes)

	if cfg.TxLeaseTTL, err = getDuration(lookup, "TX_LEASE_TTL", 30*time.Second); err != nil {
		return Config{}, err
	}

	if cfg.FeeMode != "eip1559" && cfg.FeeMode != "legacy" {
		return Config{}, fmt.Errorf("config: CHAIN_FEE_MODE must be %q or %q, got %q", "eip1559", "legacy", cfg.FeeMode)
	}
	if cfg.ChainID <= 0 {
		return Config{}, fmt.Errorf("config: CHAIN_ID must be positive, got %d", cfg.ChainID)
	}
	if cfg.Environment != EnvDevelopment && cfg.Environment != EnvProduction {
		return Config{}, fmt.Errorf("config: ENVIRONMENT must be %q or %q, got %q", EnvDevelopment, EnvProduction, cfg.Environment)
	}
	if cfg.PersistenceMode != "mongo" && cfg.PersistenceMode != "memory" {
		return Config{}, fmt.Errorf("config: PERSISTENCE_MODE must be %q or %q, got %q", "mongo", "memory", cfg.PersistenceMode)
	}
	if cfg.TxCoordinationMode != "in-process" && cfg.TxCoordinationMode != "mongo-lease" {
		return Config{}, fmt.Errorf("config: TX_COORDINATION_MODE must be %q or %q, got %q", "in-process", "mongo-lease", cfg.TxCoordinationMode)
	}
	if cfg.TxCoordinationMode == "mongo-lease" && cfg.PersistenceMode != "mongo" {
		return Config{}, fmt.Errorf("config: TX_COORDINATION_MODE=%q requires PERSISTENCE_MODE=%q (a distributed lease needs a durable shared store)", "mongo-lease", "mongo")
	}
	for name, v := range map[string]string{
		"TX_MAX_FEE_PER_GAS_WEI": cfg.MaxFeePerGasWei, "TX_MAX_TIP_PER_GAS_WEI": cfg.MaxTipPerGasWei, "TX_MAX_TOTAL_COST_WEI": cfg.MaxTxTotalCostWei,
	} {
		if v == "" {
			continue
		}
		n, ok := new(big.Int).SetString(v, 10)
		if !ok || n.Sign() < 0 {
			return Config{}, fmt.Errorf("config: %s must be a non-negative base-10 integer (wei), got %q", name, v)
		}
	}

	// An empty KYC_WEBHOOK_HMAC_SECRET makes VerifyHMAC's underlying
	// HMAC-SHA256 computable by anyone (an empty key is still a valid HMAC
	// key), so an unauthenticated caller could forge a KYC decision. A
	// configured-but-weak secret (e.g. a placeholder like "changeme") is
	// never acceptable regardless of environment; an entirely absent secret
	// is tolerated outside production as "webhook feature disabled" (see
	// cmd/platform's buildApp, which only wires compliance.WebhookService
	// when this is non-empty).
	if n := len(cfg.KYCWebhookHMACSecret); n > 0 && n < MinWebhookSecretBytes {
		return Config{}, fmt.Errorf("config: KYC_WEBHOOK_HMAC_SECRET is %d bytes, below the documented minimum of %d", n, MinWebhookSecretBytes)
	}
	// A configured-but-weak JWT signing key is never acceptable regardless of
	// environment (an admin JWT signed with a short key is trivially forgeable);
	// an entirely absent secret is tolerated outside production as "admin JWT
	// auth disabled" (buildApp only wires the admin auth service when it is set).
	if n := len(cfg.JWTSecret); n > 0 && n < MinProductionSecretBytes {
		return Config{}, fmt.Errorf("config: JWT_SECRET is %d bytes, below the documented minimum of %d", n, MinProductionSecretBytes)
	}
	// A malformed ADMIN_ADDRESS can never match a recovered signer, so it would
	// silently make admin login impossible; reject it up front in any
	// environment rather than at first login.
	if cfg.AdminAddress != "" && !isHexAddress(cfg.AdminAddress) {
		return Config{}, fmt.Errorf("config: ADMIN_ADDRESS %q is not a valid 0x-prefixed 20-byte hex address", cfg.AdminAddress)
	}
	// A malformed PROJECT_ID would be rejected later at profile create (the gate
	// compares it against the stored profile's projectId); reject it up front so
	// a typo is caught at startup, not on the first create attempt.
	if cfg.ProjectID != "" && !isUUID(cfg.ProjectID) {
		return Config{}, fmt.Errorf("config: PROJECT_ID %q is not a valid UUID", cfg.ProjectID)
	}
	if cfg.Environment == EnvProduction {
		if cfg.KYCWebhookHMACSecret == "" {
			return Config{}, errors.New("config: KYC_WEBHOOK_HMAC_SECRET must be set in production (ENVIRONMENT=production) — an empty secret would permit forged KYC decisions once relayed on-chain")
		}
		if cfg.PersistenceMode != "mongo" {
			return Config{}, fmt.Errorf("config: PERSISTENCE_MODE=%q is not allowed in production; volatile in-memory repositories would silently drop project guards, idempotency records, and audit history on restart", cfg.PersistenceMode)
		}
		// The projectId gate must be armed in production: it fixes which Asset
		// Profile this deployment will accept before anything is created, so an
		// unset value would let the first caller choose the projectId.
		if cfg.ProjectID == "" {
			return Config{}, errors.New("config: PROJECT_ID must be set in production — it fixes the deployment's projectId and gates which Asset Profile the server will accept")
		}
		// Reject test-only / plaintext hot-key providers in production.
		// kms-mock is a deterministic mock and never acceptable; raw stores
		// the private keys in plaintext config, so it is refused whenever any
		// hot key is actually configured (a keyless raw deployment — all
		// actions via wallet/multisig — stays allowed).
		if cfg.KeyProviderMode == "kms-mock" {
			return Config{}, errors.New("config: KEY_PROVIDER_MODE=kms-mock is a test-only mock secret provider and must not be used in production; use local-keystore or vault")
		}
		if cfg.KeyProviderMode == "raw" && cfg.ComplianceKeyHex != "" {
			return Config{}, errors.New("config: KEY_PROVIDER_MODE=raw keeps the compliance hot key in plaintext config; use local-keystore or vault in production")
		}
		// Admin auth is wallet-signature -> JWT (single-admin model): require a
		// configured admin wallet to authorize against and an HMAC secret to
		// sign/verify the JWT with, so admin routes are actually authenticated.
		if cfg.AdminAddress == "" {
			return Config{}, errors.New("config: ADMIN_ADDRESS must be set in production — admin routes authenticate a wallet-signature JWT against this address")
		}
		if cfg.JWTSecret == "" {
			return Config{}, errors.New("config: JWT_SECRET must be set in production so admin JWTs are signed with a real HMAC key")
		}
		// In production, require at least one genuinely independent
		// destination and validate 1 <= threshold <= destination count.
		// Without a configured backup, IPFS_API_URL
		// alone falls back to a single bare local Kubo client with no
		// replication and no durable-publication tracking at all — a
		// silent single-point-of-failure for mint evidence in production.
		if cfg.IPFSAPIURL != "" {
			backupCount := 0
			if cfg.IPFSBackupArchiveDir != "" {
				backupCount++
			}
			if cfg.IPFSBackupKuboURL != "" {
				backupCount++
			}
			if backupCount == 0 {
				return Config{}, errors.New("config: IPFS_API_URL is set but no backup destination is configured (IPFS_BACKUP_ARCHIVE_DIR or IPFS_BACKUP_KUBO_URL) — production requires at least one independent replication destination")
			}
			if cfg.IPFSReplicationThreshold < 1 || cfg.IPFSReplicationThreshold > backupCount {
				return Config{}, fmt.Errorf("config: IPFS_REPLICATION_THRESHOLD=%d must be between 1 and the configured backup destination count (%d) in production", cfg.IPFSReplicationThreshold, backupCount)
			}
		}

		// A production configuration must not accept disabled safeguards or
		// trivial privileged credentials. A syntactically
		// valid production environment must not be able to permit rapid
		// guessing of weak credentials, unbounded request bodies,
		// premature transaction finality, uncontrolled fees, or a
		// self-inflicted denial of service through a zero/negative
		// duration or limit. Every "disabled control" check below (body
		// limit, rate limit, fee cap) has a narrowly-named
		// PRODUCTION_ALLOW_* emergency override; every positive-bound
		// check does not, since a non-positive duration/burst/
		// confirmation count is never a legitimate production choice —
		// only ever a misconfiguration.
		if err := validateProductionSecrets(cfg); err != nil {
			return Config{}, err
		}
		if err := validateProductionBounds(lookup, cfg); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

func getString(lookup envLookup, key, def string) string {
	if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// getBool reads a narrowly-scoped boolean env var — used only for the
// emergency-override knobs (e.g. PRODUCTION_ALLOW_*): exactly
// "true" (case-insensitive) opts in, anything else (including unset,
// empty, or a typo) is treated as false/not-overridden — deliberately
// fail-safe rather than permissive-by-default parsing.
func getBool(lookup envLookup, key string) bool {
	v, ok := lookup(key)
	return ok && strings.EqualFold(strings.TrimSpace(v), "true")
}

// MinProductionSecretBytes is the documented minimum length for the JWT
// signing secret in production — randomly generated secrets should be at
// least 32 bytes. A length floor, same honest limitation as
// MinWebhookSecretBytes — not a true entropy measurement.
const MinProductionSecretBytes = 32

// weakProductionSecrets are known placeholder/trivial values rejected
// outright regardless of length (including the obvious single-character
// cases like "a" and "b"). Matched case-insensitively.
var weakProductionSecrets = map[string]bool{
	"a": true, "b": true, "admin": true, "operator": true, "password": true,
	"changeme": true, "change-me": true, "change_me": true, "secret": true,
	"test": true, "default": true, "placeholder": true, "example": true,
	"00000000000000000000000000000000": true,
}

// validateProductionSecrets enforces the production length/placeholder rules
// for both HMAC secrets the server holds. ADMIN_ADDRESS is a public wallet
// address, not a secret, so it is not checked here.
//
// The placeholder blocklist matters as much for the webhook secret as for the
// JWT one: every other entry in weakProductionSecrets is shorter than the
// 32-byte floor and so would be caught by length alone, but the 32-zero string
// is exactly 32 bytes and sails through it. A forged KYC decision is relayed
// on-chain by the compliance hot key, so that secret gets the same treatment.
func validateProductionSecrets(cfg Config) error {
	if len(cfg.JWTSecret) < MinProductionSecretBytes {
		return fmt.Errorf("config: JWT_SECRET must be at least %d bytes in production, got %d", MinProductionSecretBytes, len(cfg.JWTSecret))
	}
	if weakProductionSecrets[strings.ToLower(cfg.JWTSecret)] {
		return errors.New("config: JWT_SECRET is a known placeholder value and must not be used in production")
	}
	if len(cfg.KYCWebhookHMACSecret) < MinProductionSecretBytes {
		return fmt.Errorf("config: KYC_WEBHOOK_HMAC_SECRET must be at least %d bytes in production, got %d", MinProductionSecretBytes, len(cfg.KYCWebhookHMACSecret))
	}
	if weakProductionSecrets[strings.ToLower(cfg.KYCWebhookHMACSecret)] {
		return errors.New("config: KYC_WEBHOOK_HMAC_SECRET is a known placeholder value and must not be used in production")
	}
	return nil
}

// isHexAddress reports whether s is a 0x-prefixed 20-byte hex string (an
// Ethereum address), case-insensitive. A small local check so the config
// package stays free of a go-ethereum dependency for one predicate.
func isHexAddress(s string) bool {
	if len(s) != 42 || (s[0] != '0') || (s[1] != 'x' && s[1] != 'X') {
		return false
	}
	for _, c := range s[2:] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// isUUID reports whether s is a canonical 8-4-4-4-12 hex UUID string
// (case-insensitive, any version). Kept in step with the projectId check the
// assets package enforces on the profile document, so a projectId accepted at
// config load is the same shape accepted at profile create.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// validateProductionBounds enforces positive lower bounds (and, for the
// three genuinely "disabled" controls, an explicit narrowly-named
// emergency override) across every security duration, body/header limit,
// rate/burst setting, confirmation count, and fee cap.
func validateProductionBounds(lookup envLookup, cfg Config) error {
	positiveDurations := map[string]time.Duration{
		"IDEMPOTENCY_TTL":          cfg.IdempotencyTTL,
		"WALLET_CHALLENGE_TTL":     cfg.WalletChallengeTTL,
		"WALLET_SESSION_TTL":       cfg.WalletSessionTTL,
		"JWT_TTL":                  cfg.JWTTTL,
		"HTTP_READ_HEADER_TIMEOUT": cfg.HTTPReadHeaderTimeout,
		"HTTP_READ_TIMEOUT":        cfg.HTTPReadTimeout,
		"HTTP_WRITE_TIMEOUT":       cfg.HTTPWriteTimeout,
		"HTTP_IDLE_TIMEOUT":        cfg.HTTPIdleTimeout,
		"PENDING_REDEMPTION_SLA":   cfg.PendingRedemptionSLA,
		"FUNDED_CLAIM_FAILURE_SLA": cfg.FundedClaimFailureSLA,
		"TX_LEASE_TTL":             cfg.TxLeaseTTL,
	}
	for name, d := range positiveDurations {
		if d <= 0 {
			return fmt.Errorf("config: %s must be positive in production, got %s", name, d)
		}
	}

	if cfg.HTTPMaxHeaderBytes <= 0 {
		return errors.New("config: HTTP_MAX_HEADER_BYTES must be positive in production")
	}
	if cfg.Confirmations == 0 {
		return errors.New("config: CHAIN_CONFIRMATIONS must be positive in production — 0 treats a transaction as final the instant it is mined")
	}

	if cfg.MaxRequestBodyBytes <= 0 && !getBool(lookup, "PRODUCTION_ALLOW_UNBOUNDED_REQUEST_BODY") {
		return errors.New("config: MAX_REQUEST_BODY_BYTES must be positive in production (<=0 disables the request body limit) — set PRODUCTION_ALLOW_UNBOUNDED_REQUEST_BODY=true to explicitly override")
	}
	if cfg.RateLimitRPS <= 0 && !getBool(lookup, "PRODUCTION_ALLOW_DISABLED_RATE_LIMIT") {
		return errors.New("config: RATE_LIMIT_RPS must be positive in production (<=0 disables per-IP rate limiting) — set PRODUCTION_ALLOW_DISABLED_RATE_LIMIT=true to explicitly override")
	}
	if cfg.RateLimitRPS > 0 && cfg.RateLimitBurst <= 0 {
		return errors.New("config: RATE_LIMIT_BURST must be positive in production whenever RATE_LIMIT_RPS is enabled")
	}
	if cfg.MaxFeePerGasWei == "" && cfg.MaxTxTotalCostWei == "" && !getBool(lookup, "PRODUCTION_ALLOW_UNBOUNDED_FEES") {
		return errors.New("config: at least one of TX_MAX_FEE_PER_GAS_WEI or TX_MAX_TOTAL_COST_WEI must be set in production (both absent leaves transaction fees uncontrolled) — set PRODUCTION_ALLOW_UNBOUNDED_FEES=true to explicitly override")
	}
	return nil
}

func getInt64(lookup envLookup, key string, def int64) (int64, error) {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

func getFloat64(lookup envLookup, key string, def float64) (float64, error) {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

func getUint64(lookup envLookup, key string, def uint64) (uint64, error) {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

func getDuration(lookup envLookup, key string, def time.Duration) (time.Duration, error) {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s=%q: %w", key, v, err)
	}
	return d, nil
}
