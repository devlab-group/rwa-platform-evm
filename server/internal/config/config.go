// Package config loads --config YAML with shared LoadFile/LoadFromMap defaults and
// validation. Operators must protect the file's secrets with appropriate permissions.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Environment selects validation rules; startup separately checks repository and RPC
// availability
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

// MinWebhookSecretBytes bounds configured webhook-secret length, not measured entropy
const MinWebhookSecretBytes = 32

// Config holds resolved server settings
type Config struct {
	// Environment defaults to development and selects production validation
	Environment Environment

	// HTTP
	HTTPAddr string

	// Chain
	ChainRPCURL   string
	ChainID       int64
	Confirmations uint64
	FeeMode       string // "eip1559" or "legacy"
	// ExplorerURL supplies the base for transaction links; empty disables them
	ExplorerURL string

	// FactoryAddress optionally selects the deployment entry point. Indexed Project
	// records supply deployed addresses and auditor.
	FactoryAddress string
	// StartBlock is used before a checkpoint exists; 0 scans from genesis
	StartBlock uint64

	// ProjectID pins profile creation to a configured UUID and is exposed to Setup.
	// Empty disables the gate outside production; production requires it.
	ProjectID string

	// Mongo
	MongoURI string
	MongoDB  string

	// PersistenceMode selects mongo (default) or development-only memory repositories.
	// Mongo connection failure is handled separately by connectRepositories.
	PersistenceMode string

	// IPFS (Kubo HTTP API)
	IPFSAPIURL string

	// IPFSBackupArchiveDir adds filesystem replication beyond the primary Kubo node.
	// Without backups, development uses only the primary pinner.
	IPFSBackupArchiveDir string
	// IPFSBackupKuboURL adds a second Kubo destination
	IPFSBackupKuboURL string
	// IPFSReplicationThreshold sets the required successful backups, excluding the primary.
	// It is ignored when no backup is configured.
	IPFSReplicationThreshold int

	// Security
	KYCWebhookHMACSecret string
	IdempotencyTTL       time.Duration
	WalletChallengeTTL   time.Duration
	// WalletSessionTTL bounds investor sessions issued after ownership verification
	WalletSessionTTL time.Duration
	// JWTTTL bounds stateless admin JWTs issued after a wallet-signature challenge
	JWTTTL time.Duration

	// ComplianceKeyHex supplies the compliance key in raw mode; empty disables that signer.
	// Admin deployment, mint, treasury and pricing calls use operator wallets.
	ComplianceKeyHex string

	// KeyProviderMode selects raw, local-keystore, vault or kms-mock;
	// see internal/keys for required settings
	KeyProviderMode      string
	KeystoreDir          string
	KeystorePasswordFile string
	VaultAddr            string
	VaultToken           string
	VaultKeyPrefix       string
	KMSMockSeedHex       string

	// KYCProvider selects none (generic webhook only), sumsub or onfido.
	// Only selected-provider credentials are required; production checks its webhook secret.
	KYCProvider string

	KYCSumsubAppToken      string
	KYCSumsubSecretKey     string
	KYCSumsubWebhookSecret string
	KYCSumsubBaseURL       string
	KYCSumsubLevelName     string

	KYCOnfidoAPIToken     string
	KYCOnfidoWebhookToken string
	KYCOnfidoRegion       string
	KYCOnfidoWorkflowID   string
	KYCOnfidoReferrer     string

	// MaxRequestBodyBytes caps bodies; non-positive values disable it
	MaxRequestBodyBytes int64

	// RateLimitRPS and RateLimitBurst set the global per-IP bucket;
	// non-positive RPS disables it without disabling endpoint login/challenge limits
	RateLimitRPS   float64
	RateLimitBurst int

	// TrustedProxies lists trusted forwarding IPs/CIDRs. Empty uses RemoteAddr for rate
	// limits, ignoring caller-supplied forwarding headers. Behind a reverse proxy,
	// configure its actual addresses.
	TrustedProxies []string

	// CORSAllowedOrigins lists permitted scheme/host/port origins, such as the investor SPA.
	// Empty disables CORS for same-origin deployments; wildcard origins are rejected.
	CORSAllowedOrigins []string

	// MetricsAddr serves unauthenticated /metrics on a separate listener, defaulting to loopback.
	// Protect an explicitly configured remote listener with external access controls.
	MetricsAddr string

	// HTTP timeouts and header caps limit slow-client resource use on both listeners
	HTTPReadHeaderTimeout time.Duration
	HTTPReadTimeout       time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPMaxHeaderBytes    int

	// Alert thresholds for pending-redemption and funded-claim evaluators
	PendingRedemptionSLA  time.Duration
	FundedClaimFailureSLA time.Duration

	// MaxFeePerGasWei, MaxTipPerGasWei and MaxTxTotalCostWei cap signing costs
	// as base-10 wei strings; empty means no cap for that dimension
	MaxFeePerGasWei   string
	MaxTipPerGasWei   string
	MaxTxTotalCostWei string

	// TxCoordinationMode selects in-process nonce locking or mongo-lease coordination.
	// Use in-process with one process per hot key; mongo-lease requires mongo persistence.
	TxCoordinationMode string
	// TxLeaseTTL controls takeover after a nonce lease stops renewing
	TxLeaseTTL time.Duration

	// AdminAddress is the wallet authorized for signature-based API login.
	// Empty disables login outside production; production requires a valid address.
	AdminAddress string
	// JWTSecret signs admin tokens with HMAC-SHA256; empty disables JWT authentication
	// outside production, which requires MinProductionSecretBytes
	JWTSecret string
}

// envLookup supplies values without changing process-global environment
type envLookup func(key string) (string, bool)

// LoadFile strictly decodes YAML, then applies shared defaults and validation
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %q: %w", path, err)
	}
	var f fileSchema
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty document uses defaults through a zero fileSchema
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: parsing %q: %w", path, err)
	}
	return LoadFromMap(f.toEnvMap())
}

// LoadFromMap resolves explicit values through the validation engine shared with LoadFile
func LoadFromMap(env map[string]string) (Config, error) {
	return load(func(key string) (string, bool) { v, ok := env[key]; return v, ok })
}

// fileSchema maps YAML to shared validation. Pointers preserve explicit numeric/boolean
// zeroes; empty strings use defaults.
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
		CORSAllowedOrigins  []string `yaml:"cors_allowed_origins"`
	} `yaml:"http"`

	Chain struct {
		RPCURL            string  `yaml:"rpc_url"`
		ID                *int64  `yaml:"id"`
		Confirmations     *uint64 `yaml:"confirmations"`
		FeeMode           string  `yaml:"fee_mode"`
		ExplorerURL       string  `yaml:"explorer_url"`
		MaxFeePerGasWei   string  `yaml:"max_fee_per_gas_wei"`
		MaxTipPerGasWei   string  `yaml:"max_tip_per_gas_wei"`
		MaxTxTotalCostWei string  `yaml:"max_tx_total_cost_wei"`
	} `yaml:"chain"`

	// Bootstrap contract settings; deployed addresses and auditor live in the DB
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

	KYC struct {
		Provider string `yaml:"provider"`
		Sumsub   struct {
			AppToken      string `yaml:"app_token"`
			SecretKey     string `yaml:"secret_key"`
			WebhookSecret string `yaml:"webhook_secret"`
			BaseURL       string `yaml:"base_url"`
			LevelName     string `yaml:"level_name"`
		} `yaml:"sumsub"`
		Onfido struct {
			APIToken     string `yaml:"api_token"`
			WebhookToken string `yaml:"webhook_token"`
			Region       string `yaml:"region"`
			WorkflowID   string `yaml:"workflow_id"`
			Referrer     string `yaml:"referrer"`
		} `yaml:"onfido"`
	} `yaml:"kyc"`

	Tx struct {
		CoordinationMode string `yaml:"coordination_mode"`
		LeaseTTL         string `yaml:"lease_ttl"`
	} `yaml:"tx"`

	Alerts struct {
		PendingRedemptionSLA  string `yaml:"pending_redemption_sla"`
		FundedClaimFailureSLA string `yaml:"funded_claim_failure_sla"`
	} `yaml:"alerts"`

	// ProductionOverrides require explicit true to relax the named production checks
	ProductionOverrides struct {
		AllowUnboundedRequestBody *bool `yaml:"allow_unbounded_request_body"`
		AllowDisabledRateLimit    *bool `yaml:"allow_disabled_rate_limit"`
		AllowUnboundedFees        *bool `yaml:"allow_unbounded_fees"`
	} `yaml:"production_overrides"`
}

// toEnvMap preserves explicit numeric/boolean zeroes and supplied values; omitted
// settings use defaults
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
	if len(f.HTTP.CORSAllowedOrigins) > 0 {
		setStr("CORS_ALLOWED_ORIGINS", strings.Join(f.HTTP.CORSAllowedOrigins, ","))
	}

	setStr("CHAIN_RPC_URL", f.Chain.RPCURL)
	setI64("CHAIN_ID", f.Chain.ID)
	setU64("CHAIN_CONFIRMATIONS", f.Chain.Confirmations)
	setStr("CHAIN_FEE_MODE", f.Chain.FeeMode)
	setStr("CHAIN_EXPLORER_URL", f.Chain.ExplorerURL)
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

	setStr("KYC_PROVIDER", f.KYC.Provider)
	setStr("KYC_SUMSUB_APP_TOKEN", f.KYC.Sumsub.AppToken)
	setStr("KYC_SUMSUB_SECRET_KEY", f.KYC.Sumsub.SecretKey)
	setStr("KYC_SUMSUB_WEBHOOK_SECRET", f.KYC.Sumsub.WebhookSecret)
	setStr("KYC_SUMSUB_BASE_URL", f.KYC.Sumsub.BaseURL)
	setStr("KYC_SUMSUB_LEVEL_NAME", f.KYC.Sumsub.LevelName)
	setStr("KYC_ONFIDO_API_TOKEN", f.KYC.Onfido.APIToken)
	setStr("KYC_ONFIDO_WEBHOOK_TOKEN", f.KYC.Onfido.WebhookToken)
	setStr("KYC_ONFIDO_REGION", f.KYC.Onfido.Region)
	setStr("KYC_ONFIDO_WORKFLOW_ID", f.KYC.Onfido.WorkflowID)
	setStr("KYC_ONFIDO_REFERRER", f.KYC.Onfido.Referrer)

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
		ExplorerURL:          strings.TrimRight(getString(lookup, "CHAIN_EXPLORER_URL", ""), "/"),
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

		KYCProvider:            getString(lookup, "KYC_PROVIDER", "none"),
		KYCSumsubAppToken:      getString(lookup, "KYC_SUMSUB_APP_TOKEN", ""),
		KYCSumsubSecretKey:     getString(lookup, "KYC_SUMSUB_SECRET_KEY", ""),
		KYCSumsubWebhookSecret: getString(lookup, "KYC_SUMSUB_WEBHOOK_SECRET", ""),
		KYCSumsubBaseURL:       getString(lookup, "KYC_SUMSUB_BASE_URL", ""),
		KYCSumsubLevelName:     getString(lookup, "KYC_SUMSUB_LEVEL_NAME", ""),
		KYCOnfidoAPIToken:      getString(lookup, "KYC_ONFIDO_API_TOKEN", ""),
		KYCOnfidoWebhookToken:  getString(lookup, "KYC_ONFIDO_WEBHOOK_TOKEN", ""),
		KYCOnfidoRegion:        getString(lookup, "KYC_ONFIDO_REGION", "eu"),
		KYCOnfidoWorkflowID:    getString(lookup, "KYC_ONFIDO_WORKFLOW_ID", ""),
		KYCOnfidoReferrer:      getString(lookup, "KYC_ONFIDO_REFERRER", ""),

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

	if v, ok := lookup("CORS_ALLOWED_ORIGINS"); ok && strings.TrimSpace(v) != "" {
		var origins []string
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
		cfg.CORSAllowedOrigins = origins
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
	if cfg.ExplorerURL != "" {
		// Accept an absolute HTTP(S) base without query, fragment or credentials
		// before transaction paths are appended
		u, err := url.Parse(cfg.ExplorerURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return Config{}, fmt.Errorf("config: CHAIN_EXPLORER_URL must be an absolute http(s) URL with no query, fragment or credentials, got %q", cfg.ExplorerURL)
		}
	}
	if cfg.ChainID <= 0 {
		return Config{}, fmt.Errorf("config: CHAIN_ID must be positive, got %d", cfg.ChainID)
	}
	// Validate explicit browser origins in every environment and reject wildcards
	for _, origin := range cfg.CORSAllowedOrigins {
		switch {
		case origin == "*":
			return Config{}, errors.New(`config: http.cors_allowed_origins must not contain "*" - this API accepts an Authorization bearer, and a wildcard origin would let any site drive an authenticated session from a victim's browser; list the exact origins instead`)
		case strings.HasSuffix(origin, "/"):
			return Config{}, fmt.Errorf("config: http.cors_allowed_origins %q must not have a trailing slash - a browser's Origin header is scheme+host+port only, so this would never match", origin)
		case !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://"):
			return Config{}, fmt.Errorf("config: http.cors_allowed_origins %q must be a full origin including the scheme, e.g. %q", origin, "http://localhost:5173")
		}
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
	// Require the selected provider's credentials in every environment;
	// production applies additional secret checks
	switch cfg.KYCProvider {
	case "none":
		// generic KYCWebhookHMACSecret path; nothing provider-specific required
	case "sumsub":
		for name, v := range map[string]string{
			"KYC_SUMSUB_APP_TOKEN":      cfg.KYCSumsubAppToken,
			"KYC_SUMSUB_SECRET_KEY":     cfg.KYCSumsubSecretKey,
			"KYC_SUMSUB_WEBHOOK_SECRET": cfg.KYCSumsubWebhookSecret,
		} {
			if v == "" {
				return Config{}, fmt.Errorf("config: %s is required when KYC_PROVIDER=sumsub", name)
			}
		}
	case "onfido":
		for name, v := range map[string]string{
			"KYC_ONFIDO_API_TOKEN":     cfg.KYCOnfidoAPIToken,
			"KYC_ONFIDO_WEBHOOK_TOKEN": cfg.KYCOnfidoWebhookToken,
			"KYC_ONFIDO_WORKFLOW_ID":   cfg.KYCOnfidoWorkflowID,
		} {
			if v == "" {
				return Config{}, fmt.Errorf("config: %s is required when KYC_PROVIDER=onfido", name)
			}
		}
		switch cfg.KYCOnfidoRegion {
		case "eu", "us", "ca":
		default:
			return Config{}, fmt.Errorf("config: KYC_ONFIDO_REGION must be eu, us, or ca, got %q", cfg.KYCOnfidoRegion)
		}
	default:
		return Config{}, fmt.Errorf("config: KYC_PROVIDER must be %q, %q, or %q, got %q", "none", "sumsub", "onfido", cfg.KYCProvider)
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

	// Reject short configured webhook secrets; absence disables generic KYC outside production
	if n := len(cfg.KYCWebhookHMACSecret); n > 0 && n < MinWebhookSecretBytes {
		return Config{}, fmt.Errorf("config: KYC_WEBHOOK_HMAC_SECRET is %d bytes, below the documented minimum of %d", n, MinWebhookSecretBytes)
	}
	// Reject short configured JWT secrets; absence disables admin authentication outside production
	if n := len(cfg.JWTSecret); n > 0 && n < MinProductionSecretBytes {
		return Config{}, fmt.Errorf("config: JWT_SECRET is %d bytes, below the documented minimum of %d", n, MinProductionSecretBytes)
	}
	// Reject malformed admin addresses at configuration load
	if cfg.AdminAddress != "" && !isHexAddress(cfg.AdminAddress) {
		return Config{}, fmt.Errorf("config: ADMIN_ADDRESS %q is not a valid 0x-prefixed 20-byte hex address", cfg.AdminAddress)
	}
	// Reject a malformed project UUID before profile creation
	if cfg.ProjectID != "" && !isUUID(cfg.ProjectID) {
		return Config{}, fmt.Errorf("config: PROJECT_ID %q is not a valid UUID", cfg.ProjectID)
	}
	if cfg.Environment == EnvProduction {
		// Validate the active webhook secret. Sumsub uses the operator-secret length floor;
		// Onfido's provider-issued token must be present and not a known placeholder.
		switch cfg.KYCProvider {
		case "none":
			if cfg.KYCWebhookHMACSecret == "" {
				return Config{}, errors.New("config: KYC_WEBHOOK_HMAC_SECRET must be set in production (ENVIRONMENT=production) - an empty secret would permit forged KYC decisions once relayed on-chain")
			}
		case "sumsub":
			if len(cfg.KYCSumsubWebhookSecret) < MinProductionSecretBytes {
				return Config{}, fmt.Errorf("config: KYC_SUMSUB_WEBHOOK_SECRET must be at least %d bytes in production, got %d", MinProductionSecretBytes, len(cfg.KYCSumsubWebhookSecret))
			}
			if weakProductionSecrets[strings.ToLower(cfg.KYCSumsubWebhookSecret)] {
				return Config{}, errors.New("config: KYC_SUMSUB_WEBHOOK_SECRET is a known placeholder value and must not be used in production")
			}
		case "onfido":
			if weakProductionSecrets[strings.ToLower(cfg.KYCOnfidoWebhookToken)] {
				return Config{}, errors.New("config: KYC_ONFIDO_WEBHOOK_TOKEN is a known placeholder value and must not be used in production")
			}
		}
		if cfg.PersistenceMode != "mongo" {
			return Config{}, fmt.Errorf("config: PERSISTENCE_MODE=%q is not allowed in production; volatile in-memory repositories would silently drop project guards, idempotency records, and audit history on restart", cfg.PersistenceMode)
		}
		// Production must pin the project UUID before accepting a profile
		if cfg.ProjectID == "" {
			return Config{}, errors.New("config: PROJECT_ID must be set in production - it fixes the deployment's projectId and gates which Asset Profile the server will accept")
		}
		// Reject kms-mock and configured raw keys in production; keyless raw mode remains allowed
		if cfg.KeyProviderMode == "kms-mock" {
			return Config{}, errors.New("config: KEY_PROVIDER_MODE=kms-mock is a test-only mock secret provider and must not be used in production; use local-keystore or vault")
		}
		if cfg.KeyProviderMode == "raw" && cfg.ComplianceKeyHex != "" {
			return Config{}, errors.New("config: KEY_PROVIDER_MODE=raw keeps the compliance hot key in plaintext config; use local-keystore or vault in production")
		}
		// Require the production login wallet and JWT signing secret
		if cfg.AdminAddress == "" {
			return Config{}, errors.New("config: ADMIN_ADDRESS must be set in production - admin routes authenticate a wallet-signature JWT against this address")
		}
		if cfg.JWTSecret == "" {
			return Config{}, errors.New("config: JWT_SECRET must be set in production so admin JWTs are signed with a real HMAC key")
		}
		// For production IPFS publication, require a backup and a threshold within its count.
		// Operators must ensure destinations are protected independently.
		if cfg.IPFSAPIURL != "" {
			backupCount := 0
			if cfg.IPFSBackupArchiveDir != "" {
				backupCount++
			}
			if cfg.IPFSBackupKuboURL != "" {
				backupCount++
			}
			if backupCount == 0 {
				return Config{}, errors.New("config: IPFS_API_URL is set but no backup destination is configured (IPFS_BACKUP_ARCHIVE_DIR or IPFS_BACKUP_KUBO_URL) - production requires at least one independent replication destination")
			}
			if cfg.IPFSReplicationThreshold < 1 || cfg.IPFSReplicationThreshold > backupCount {
				return Config{}, fmt.Errorf("config: IPFS_REPLICATION_THRESHOLD=%d must be between 1 and the configured backup destination count (%d) in production", cfg.IPFSReplicationThreshold, backupCount)
			}
		}

		// Enforce production secret and positive-bound rules.
		// Only named body, rate and fee controls have explicit emergency overrides.
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

// getBool opts in only for case-insensitive true; absent or malformed values mean false
func getBool(lookup envLookup, key string) bool {
	v, ok := lookup(key)
	return ok && strings.EqualFold(strings.TrimSpace(v), "true")
}

// MinProductionSecretBytes is the 32-byte production secret floor, not an entropy measurement
const MinProductionSecretBytes = 32

// weakProductionSecrets lists placeholders checked case-insensitively, regardless of length
var weakProductionSecrets = map[string]bool{
	"a": true, "b": true, "admin": true, "operator": true, "password": true,
	"changeme": true, "change-me": true, "change_me": true, "secret": true,
	"test": true, "default": true, "placeholder": true, "example": true,
	"00000000000000000000000000000000": true,
}

// validateProductionSecrets enforces JWT and active generic-webhook minimum lengths,
// rejecting blocklisted placeholders even above the floor
func validateProductionSecrets(cfg Config) error {
	if len(cfg.JWTSecret) < MinProductionSecretBytes {
		return fmt.Errorf("config: JWT_SECRET must be at least %d bytes in production, got %d", MinProductionSecretBytes, len(cfg.JWTSecret))
	}
	if weakProductionSecrets[strings.ToLower(cfg.JWTSecret)] {
		return errors.New("config: JWT_SECRET is a known placeholder value and must not be used in production")
	}
	// Generic secret validation applies only in none mode
	if cfg.KYCProvider == "none" {
		if len(cfg.KYCWebhookHMACSecret) < MinProductionSecretBytes {
			return fmt.Errorf("config: KYC_WEBHOOK_HMAC_SECRET must be at least %d bytes in production, got %d", MinProductionSecretBytes, len(cfg.KYCWebhookHMACSecret))
		}
		if weakProductionSecrets[strings.ToLower(cfg.KYCWebhookHMACSecret)] {
			return errors.New("config: KYC_WEBHOOK_HMAC_SECRET is a known placeholder value and must not be used in production")
		}
	}
	return nil
}

// isHexAddress checks 0x-prefixed 20-byte hex without adding a go-ethereum dependency
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

// isUUID matches the profile's 8-4-4-4-12 hex identity shape, case-insensitively
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

// validateProductionBounds checks durations, sizes, rate settings, finality and fee caps,
// allowing only named overrides for disabled controls
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
		return errors.New("config: CHAIN_CONFIRMATIONS must be positive in production - 0 treats a transaction as final the instant it is mined")
	}

	if cfg.MaxRequestBodyBytes <= 0 && !getBool(lookup, "PRODUCTION_ALLOW_UNBOUNDED_REQUEST_BODY") {
		return errors.New("config: MAX_REQUEST_BODY_BYTES must be positive in production (<=0 disables the request body limit) - set PRODUCTION_ALLOW_UNBOUNDED_REQUEST_BODY=true to explicitly override")
	}
	if cfg.RateLimitRPS <= 0 && !getBool(lookup, "PRODUCTION_ALLOW_DISABLED_RATE_LIMIT") {
		return errors.New("config: RATE_LIMIT_RPS must be positive in production (<=0 disables per-IP rate limiting) - set PRODUCTION_ALLOW_DISABLED_RATE_LIMIT=true to explicitly override")
	}
	if cfg.RateLimitRPS > 0 && cfg.RateLimitBurst <= 0 {
		return errors.New("config: RATE_LIMIT_BURST must be positive in production whenever RATE_LIMIT_RPS is enabled")
	}
	if cfg.MaxFeePerGasWei == "" && cfg.MaxTxTotalCostWei == "" && !getBool(lookup, "PRODUCTION_ALLOW_UNBOUNDED_FEES") {
		return errors.New("config: at least one of TX_MAX_FEE_PER_GAS_WEI or TX_MAX_TOTAL_COST_WEI must be set in production (both absent leaves transaction fees uncontrolled) - set PRODUCTION_ALLOW_UNBOUNDED_FEES=true to explicitly override")
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
