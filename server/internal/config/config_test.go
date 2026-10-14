package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.ChainID != 31337 {
		t.Errorf("ChainID = %d, want 31337", cfg.ChainID)
	}
	if cfg.Confirmations != 3 {
		t.Errorf("Confirmations = %d, want 3", cfg.Confirmations)
	}
	if cfg.FeeMode != "eip1559" {
		t.Errorf("FeeMode = %q, want eip1559", cfg.FeeMode)
	}
	if cfg.MongoURI != "mongodb://127.0.0.1:27017" {
		t.Errorf("MongoURI = %q", cfg.MongoURI)
	}
	if cfg.IdempotencyTTL != 24*time.Hour {
		t.Errorf("IdempotencyTTL = %v, want 24h", cfg.IdempotencyTTL)
	}
	if cfg.WalletChallengeTTL != 15*time.Minute {
		t.Errorf("WalletChallengeTTL = %v, want 15m", cfg.WalletChallengeTTL)
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"HTTP_ADDR":            ":9090",
		"CHAIN_ID":             "1",
		"CHAIN_CONFIRMATIONS":  "12",
		"CHAIN_FEE_MODE":       "legacy",
		"MONGO_URI":            "mongodb://mongo:27017",
		"IDEMPOTENCY_TTL":      "1h",
		"WALLET_CHALLENGE_TTL": "5m",
	}
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.ChainID != 1 {
		t.Errorf("ChainID = %d", cfg.ChainID)
	}
	if cfg.Confirmations != 12 {
		t.Errorf("Confirmations = %d", cfg.Confirmations)
	}
	if cfg.FeeMode != "legacy" {
		t.Errorf("FeeMode = %q", cfg.FeeMode)
	}
	if cfg.IdempotencyTTL != time.Hour {
		t.Errorf("IdempotencyTTL = %v", cfg.IdempotencyTTL)
	}
	if cfg.WalletChallengeTTL != 5*time.Minute {
		t.Errorf("WalletChallengeTTL = %v", cfg.WalletChallengeTTL)
	}
}

func TestLoadDefaultsTrustNoProxies(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TrustedProxies != nil {
		t.Errorf("TrustedProxies = %v, want nil by default (trust no proxies — see the field's doc comment)", cfg.TrustedProxies)
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{"TRUSTED_PROXIES": "10.0.0.1, 172.16.0.0/12"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"10.0.0.1", "172.16.0.0/12"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", cfg.TrustedProxies, want)
	}
	for i, w := range want {
		if cfg.TrustedProxies[i] != w {
			t.Errorf("TrustedProxies[%d] = %q, want %q", i, cfg.TrustedProxies[i], w)
		}
	}
}

// TestLoadCORSAllowedOrigins trims and splits origin lists, dropping empty entries
func TestLoadCORSAllowedOrigins(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{
		"CORS_ALLOWED_ORIGINS": " http://localhost:5173 , https://investor.example ,, ",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"http://localhost:5173", "https://investor.example"}
	if len(cfg.CORSAllowedOrigins) != len(want) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, want)
	}
	for i, w := range want {
		if cfg.CORSAllowedOrigins[i] != w {
			t.Errorf("origin %d = %q, want %q", i, cfg.CORSAllowedOrigins[i], w)
		}
	}
}

// TestLoadCORSDefaultsToDisabled keeps same-origin deployments from enabling CORS implicitly
func TestLoadCORSDefaultsToDisabled(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.CORSAllowedOrigins) != 0 {
		t.Errorf("CORSAllowedOrigins = %v, want empty by default", cfg.CORSAllowedOrigins)
	}
}

// TestLoadRejectsBadCORSOrigins rejects wildcards and malformed origins in every environment
func TestLoadRejectsBadCORSOrigins(t *testing.T) {
	for name, value := range map[string]string{
		"wildcard":              "*",
		"wildcard among others": "http://localhost:5173,*",
		"trailing slash":        "http://localhost:5173/",
		"missing scheme":        "localhost:5173",
		"bare host":             "investor.example",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFromMap(map[string]string{"CORS_ALLOWED_ORIGINS": value}); err == nil {
				t.Fatalf("expected load to reject CORS_ALLOWED_ORIGINS=%q", value)
			}
		})
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := map[string]map[string]string{
		"bad chain id":          {"CHAIN_ID": "not-a-number"},
		"zero chain id":         {"CHAIN_ID": "0"},
		"bad confirmations":     {"CHAIN_CONFIRMATIONS": "-1"},
		"bad fee mode":          {"CHAIN_FEE_MODE": "bogus"},
		"explorer url script":   {"CHAIN_EXPLORER_URL": "javascript:alert(1)"},
		"explorer url relative": {"CHAIN_EXPLORER_URL": "etherscan.io"},
		"explorer url query":    {"CHAIN_EXPLORER_URL": "https://etherscan.io/?network=x"},
		"bad idempotency ttl":   {"IDEMPOTENCY_TTL": "not-a-duration"},
		"bad environment":       {"ENVIRONMENT": "staging"},
		"bad persistence mode":  {"PERSISTENCE_MODE": "redis"},
		"bad tx coordination":   {"TX_COORDINATION_MODE": "raft"},
		"bad tx lease ttl":      {"TX_LEASE_TTL": "not-a-duration"},
		"bad kyc provider":      {"KYC_PROVIDER": "jumio"},
		"sumsub missing app token": {"KYC_PROVIDER": "sumsub",
			"KYC_SUMSUB_SECRET_KEY": "s", "KYC_SUMSUB_WEBHOOK_SECRET": "w"},
		"onfido missing workflow": {"KYC_PROVIDER": "onfido",
			"KYC_ONFIDO_API_TOKEN": "t", "KYC_ONFIDO_WEBHOOK_TOKEN": "w"},
		"onfido bad region": {"KYC_PROVIDER": "onfido", "KYC_ONFIDO_API_TOKEN": "t",
			"KYC_ONFIDO_WEBHOOK_TOKEN": "w", "KYC_ONFIDO_WORKFLOW_ID": "wf", "KYC_ONFIDO_REGION": "antarctica"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

// TestLoadMongoLeaseRequiresMongoPersistence requires shared durable lease storage
func TestLoadMongoLeaseRequiresMongoPersistence(t *testing.T) {
	_, err := LoadFromMap(map[string]string{
		"TX_COORDINATION_MODE": "mongo-lease",
		"PERSISTENCE_MODE":     "memory",
	})
	if err == nil {
		t.Fatal("expected an error for mongo-lease coordination with memory persistence")
	}
}

// TestLoadMongoLeaseAcceptedWithMongoPersistence accepts the supported backend pairing
func TestLoadMongoLeaseAcceptedWithMongoPersistence(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{
		"TX_COORDINATION_MODE": "mongo-lease",
		"PERSISTENCE_MODE":     "mongo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TxCoordinationMode != "mongo-lease" {
		t.Fatalf("TxCoordinationMode = %q, want mongo-lease", cfg.TxCoordinationMode)
	}
	if cfg.TxLeaseTTL <= 0 {
		t.Fatalf("expected a positive default TxLeaseTTL, got %v", cfg.TxLeaseTTL)
	}
}

// TestLoadDefaultsAreDevelopment keeps production checks opt-in through environment selection
func TestLoadDefaultsAreDevelopment(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != EnvDevelopment {
		t.Errorf("Environment = %q, want %q", cfg.Environment, EnvDevelopment)
	}
	if cfg.PersistenceMode != "mongo" {
		t.Errorf("PersistenceMode = %q, want mongo", cfg.PersistenceMode)
	}
}

// TestLoadDevelopmentAllowsEmptyWebhookSecret permits disabling generic KYC outside production
func TestLoadDevelopmentAllowsEmptyWebhookSecret(t *testing.T) {
	if _, err := LoadFromMap(map[string]string{"ENVIRONMENT": "development"}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadRejectsWeakWebhookSecretAnyEnvironment applies the configured-secret length floor
func TestLoadRejectsWeakWebhookSecretAnyEnvironment(t *testing.T) {
	_, err := LoadFromMap(map[string]string{"KYC_WEBHOOK_HMAC_SECRET": "too-short"})
	if err == nil {
		t.Fatal("expected error for a below-minimum-length webhook secret")
	}
}

// TestLoadProductionRequiresWebhookSecret rejects a missing active generic secret
func TestLoadProductionRequiresWebhookSecret(t *testing.T) {
	_, err := LoadFromMap(map[string]string{"ENVIRONMENT": "production"})
	if err == nil {
		t.Fatal("expected production startup to fail with no KYC_WEBHOOK_HMAC_SECRET configured")
	}
}

// TestLoadProductionAcceptsStrongWebhookSecret accepts the fixture's valid production settings
func prodEnv() map[string]string {
	return map[string]string{
		"ENVIRONMENT":             "production",
		"KYC_WEBHOOK_HMAC_SECRET": "01234567890123456789012345678901", // 32 bytes
		"ADMIN_ADDRESS":           "0x00000000000000000000000000000000000000A1",
		"JWT_SECRET":              "jwt-secret-0000000000000000000000000000000",
		"PROJECT_ID":              "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		// Configure one backup; loading validates settings without accessing this path
		"IPFS_BACKUP_ARCHIVE_DIR": "./testdata/ipfs-archive",
		// Provide a production fee cap
		"TX_MAX_FEE_PER_GAS_WEI": "500000000000",
	}
}

func TestLoadProductionAcceptsStrongWebhookSecret(t *testing.T) {
	if _, err := LoadFromMap(prodEnv()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadProductionSumsubDoesNotRequireGenericSecret validates the active Sumsub secret
// without requiring an unused generic secret
func TestLoadProductionSumsubProvider(t *testing.T) {
	env := prodEnv()
	delete(env, "KYC_WEBHOOK_HMAC_SECRET") // not needed when a provider is configured
	env["KYC_PROVIDER"] = "sumsub"
	env["KYC_SUMSUB_APP_TOKEN"] = "app-token"
	env["KYC_SUMSUB_SECRET_KEY"] = "secret-key"
	// too-short webhook secret must fail
	env["KYC_SUMSUB_WEBHOOK_SECRET"] = "short"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected failure for a weak Sumsub webhook secret in production")
	}
	// a strong one passes with no generic secret present
	env["KYC_SUMSUB_WEBHOOK_SECRET"] = "01234567890123456789012345678901234"
	cfg, err := LoadFromMap(env)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.KYCProvider != "sumsub" {
		t.Fatalf("KYCProvider = %q, want sumsub", cfg.KYCProvider)
	}
}

// TestLoadProductionRequiresAdminAuth requires both the login wallet and JWT signing secret
func TestLoadProductionRequiresAdminAuth(t *testing.T) {
	env := prodEnv()
	delete(env, "ADMIN_ADDRESS")
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail without ADMIN_ADDRESS")
	}
	env = prodEnv()
	delete(env, "JWT_SECRET")
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail without JWT_SECRET")
	}
	env = prodEnv()
	env["ADMIN_ADDRESS"] = "not-a-hex-address"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with a malformed ADMIN_ADDRESS")
	}
}

// TestLoadProductionRejectsMockAndRawKeyProviders rejects mock providers and configured raw keys
func TestLoadProductionRejectsMockAndRawKeyProviders(t *testing.T) {
	env := prodEnv()
	env["KEY_PROVIDER_MODE"] = "kms-mock"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with KEY_PROVIDER_MODE=kms-mock")
	}
	env = prodEnv()
	env["KEY_PROVIDER_MODE"] = "raw"
	env["COMPLIANCE_KEY"] = "0x" + "11"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with a raw-mode hot key configured")
	}
}

// TestLoadProductionRejectsMemoryPersistence requires durable repositories
func TestLoadProductionRejectsMemoryPersistence(t *testing.T) {
	env := map[string]string{
		"ENVIRONMENT":             "production",
		"KYC_WEBHOOK_HMAC_SECRET": "01234567890123456789012345678901",
		"PERSISTENCE_MODE":        "memory",
	}
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with PERSISTENCE_MODE=memory")
	}
}

// TestLoadProductionRequiresProjectID requires a pinned profile identity before startup
func TestLoadProductionRequiresProjectID(t *testing.T) {
	env := prodEnv()
	delete(env, "PROJECT_ID")
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with no PROJECT_ID set")
	}
}

// TestLoadRejectsMalformedProjectID rejects invalid UUID settings at load
func TestLoadRejectsMalformedProjectID(t *testing.T) {
	env := map[string]string{"PROJECT_ID": "not-a-uuid"}
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected load to reject a malformed PROJECT_ID")
	}
}

// TestLoadProductionRequiresIPFSBackupDestination rejects primary-only publication settings
func TestLoadProductionRequiresIPFSBackupDestination(t *testing.T) {
	env := prodEnv()
	delete(env, "IPFS_BACKUP_ARCHIVE_DIR")
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with IPFS_API_URL enabled but no backup destination configured")
	}
}

// TestLoadProductionRejectsReplicationThresholdOutOfRange enforces 1 through backup count
func TestLoadProductionRejectsReplicationThresholdOutOfRange(t *testing.T) {
	tests := map[string]string{"zero": "0", "negative": "-1", "exceeds destination count": "2"}
	for name, threshold := range tests {
		t.Run(name, func(t *testing.T) {
			env := prodEnv() // exactly one backup destination configured
			env["IPFS_REPLICATION_THRESHOLD"] = threshold
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected production startup to fail with IPFS_REPLICATION_THRESHOLD=%s and 1 configured backup", threshold)
			}
		})
	}
}

// TestLoadProductionAcceptsReplicationThresholdWithinRange includes multiple backups
func TestLoadProductionAcceptsReplicationThresholdWithinRange(t *testing.T) {
	env := prodEnv()
	env["IPFS_BACKUP_KUBO_URL"] = "http://backup-kubo:5001"
	env["IPFS_REPLICATION_THRESHOLD"] = "2"
	if _, err := LoadFromMap(env); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadDevelopmentAllowsMemoryPersistence permits explicit local/test memory mode
func TestLoadDevelopmentAllowsMemoryPersistence(t *testing.T) {
	if _, err := LoadFromMap(map[string]string{"PERSISTENCE_MODE": "memory"}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadDefaultMetricsAddrIsLoopback limits default metrics exposure
func TestLoadDefaultMetricsAddrIsLoopback(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MetricsAddr != "127.0.0.1:9090" {
		t.Errorf("MetricsAddr = %q, want 127.0.0.1:9090", cfg.MetricsAddr)
	}
}

// TestLoadDefaultHTTPHardening checks non-zero timeout and header-size defaults
func TestLoadDefaultHTTPHardening(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPReadHeaderTimeout <= 0 {
		t.Error("HTTPReadHeaderTimeout must default to a positive duration")
	}
	if cfg.HTTPReadTimeout <= 0 {
		t.Error("HTTPReadTimeout must default to a positive duration")
	}
	if cfg.HTTPWriteTimeout <= 0 {
		t.Error("HTTPWriteTimeout must default to a positive duration")
	}
	if cfg.HTTPIdleTimeout <= 0 {
		t.Error("HTTPIdleTimeout must default to a positive duration")
	}
	if cfg.HTTPMaxHeaderBytes <= 0 {
		t.Error("HTTPMaxHeaderBytes must default to a positive value")
	}
}

// TestLoadFeeCapsMustBeNonNegativeIntegers checks decimal wei inputs
func TestLoadFeeCapsMustBeNonNegativeIntegers(t *testing.T) {
	for _, key := range []string{"TX_MAX_FEE_PER_GAS_WEI", "TX_MAX_TIP_PER_GAS_WEI", "TX_MAX_TOTAL_COST_WEI"} {
		t.Run(key, func(t *testing.T) {
			if _, err := LoadFromMap(map[string]string{key: "not-a-number"}); err == nil {
				t.Fatalf("expected error for non-numeric %s", key)
			}
			if _, err := LoadFromMap(map[string]string{key: "-5"}); err == nil {
				t.Fatalf("expected error for negative %s", key)
			}
			if _, err := LoadFromMap(map[string]string{key: "1000000000"}); err != nil {
				t.Fatalf("Load() error for valid %s = %v", key, err)
			}
		})
	}
}

// --- Production config hardening ---

// TestLoadProductionRejectsTrivialSecrets covers weak placeholders and short JWT keys
func TestLoadProductionRejectsTrivialSecrets(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"single char a", "a"},
		{"single char b", "b"},
		{"changeme", "changeme"},
		{"CHANGEME uppercase", "CHANGEME"},
		{"password", "password"},
		{"admin literal", "admin"},
		{"31 bytes (one short)", strings.Repeat("x", 31)},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run("jwt/"+tt.name, func(t *testing.T) {
			env := prodEnv()
			env["JWT_SECRET"] = tt.value
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected production startup to reject JWT_SECRET=%q", tt.value)
			}
		})
	}
}

// TestLoadProductionAcceptsStrongSecrets accepts the valid secret fixture
func TestLoadProductionAcceptsStrongSecrets(t *testing.T) {
	if _, err := LoadFromMap(prodEnv()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadProductionRejectsNonPositiveDurationsAndLimits varies one invalid bound
// at a time against an otherwise valid configuration
func TestLoadProductionRejectsNonPositiveDurationsAndLimits(t *testing.T) {
	tests := map[string]string{
		"IDEMPOTENCY_TTL":          "0",
		"WALLET_CHALLENGE_TTL":     "-1s",
		"WALLET_SESSION_TTL":       "0",
		"JWT_TTL":                  "0",
		"HTTP_READ_HEADER_TIMEOUT": "0",
		"HTTP_READ_TIMEOUT":        "0",
		"HTTP_WRITE_TIMEOUT":       "0",
		"HTTP_IDLE_TIMEOUT":        "0",
		"PENDING_REDEMPTION_SLA":   "0",
		"FUNDED_CLAIM_FAILURE_SLA": "0",
		"TX_LEASE_TTL":             "0",
		"HTTP_MAX_HEADER_BYTES":    "0",
		"CHAIN_CONFIRMATIONS":      "0",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			env := prodEnv()
			env[key] = value
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected production startup to reject %s=%q", key, value)
			}
		})
	}
}

// TestLoadProductionDisabledControlsRequireExplicitOverride requires the named
// override for disabled body, rate or fee controls
func TestLoadProductionDisabledControlsRequireExplicitOverride(t *testing.T) {
	tests := []struct {
		name        string
		disableKey  string
		disableVal  string
		overrideKey string
	}{
		{"request body limit", "MAX_REQUEST_BODY_BYTES", "0", "PRODUCTION_ALLOW_UNBOUNDED_REQUEST_BODY"},
		{"rate limit", "RATE_LIMIT_RPS", "0", "PRODUCTION_ALLOW_DISABLED_RATE_LIMIT"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/rejected without override", func(t *testing.T) {
			env := prodEnv()
			env[tt.disableKey] = tt.disableVal
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected production startup to reject %s=%s without an override", tt.disableKey, tt.disableVal)
			}
		})
		t.Run(tt.name+"/accepted with override", func(t *testing.T) {
			env := prodEnv()
			env[tt.disableKey] = tt.disableVal
			env[tt.overrideKey] = "true"
			if _, err := LoadFromMap(env); err != nil {
				t.Fatalf("expected the explicit override to be accepted: %v", err)
			}
		})
	}

	t.Run("fee caps/rejected without override", func(t *testing.T) {
		env := prodEnv()
		delete(env, "TX_MAX_FEE_PER_GAS_WEI")
		if _, err := LoadFromMap(env); err == nil {
			t.Fatal("expected production startup to reject fully-absent fee caps without an override")
		}
	})
	t.Run("fee caps/accepted with override", func(t *testing.T) {
		env := prodEnv()
		delete(env, "TX_MAX_FEE_PER_GAS_WEI")
		env["PRODUCTION_ALLOW_UNBOUNDED_FEES"] = "true"
		if _, err := LoadFromMap(env); err != nil {
			t.Fatalf("expected the explicit override to be accepted: %v", err)
		}
	})
}

// TestLoadProductionRejectsZeroBurstWhenRateLimitEnabled checks positive-RPS burst bounds
func TestLoadProductionRejectsZeroBurstWhenRateLimitEnabled(t *testing.T) {
	env := prodEnv()
	env["RATE_LIMIT_RPS"] = "50"
	env["RATE_LIMIT_BURST"] = "0"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to reject RATE_LIMIT_BURST=0 with RATE_LIMIT_RPS enabled")
	}
}

// TestLoadProductionRejectsOverflowAndGarbageDurations checks parsing before semantic bounds
func TestLoadProductionRejectsOverflowAndGarbageDurations(t *testing.T) {
	tests := map[string]string{
		"HTTP_MAX_HEADER_BYTES": "99999999999999999999999999", // overflows int64
		"WALLET_SESSION_TTL":    "not-a-duration",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			env := prodEnv()
			env[key] = value
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected production startup to reject %s=%q", key, value)
			}
		})
	}
}

func TestLoadExplorerURLTrimsTrailingSlash(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{"CHAIN_EXPLORER_URL": "https://sepolia.etherscan.io/"})
	if err != nil {
		t.Fatalf("LoadFromMap: %v", err)
	}
	if cfg.ExplorerURL != "https://sepolia.etherscan.io" {
		t.Fatalf("ExplorerURL = %q", cfg.ExplorerURL)
	}
}
