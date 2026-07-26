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

func TestLoadInvalid(t *testing.T) {
	tests := map[string]map[string]string{
		"bad chain id":         {"CHAIN_ID": "not-a-number"},
		"zero chain id":        {"CHAIN_ID": "0"},
		"bad confirmations":    {"CHAIN_CONFIRMATIONS": "-1"},
		"bad fee mode":         {"CHAIN_FEE_MODE": "bogus"},
		"bad idempotency ttl":  {"IDEMPOTENCY_TTL": "not-a-duration"},
		"bad environment":      {"ENVIRONMENT": "staging"},
		"bad persistence mode": {"PERSISTENCE_MODE": "redis"},
		"bad tx coordination":  {"TX_COORDINATION_MODE": "raft"},
		"bad tx lease ttl":     {"TX_LEASE_TTL": "not-a-duration"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFromMap(env); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

// TestLoadMongoLeaseRequiresMongoPersistence: a distributed lease with no
// durable shared backing store cannot coordinate anything.
func TestLoadMongoLeaseRequiresMongoPersistence(t *testing.T) {
	_, err := LoadFromMap(map[string]string{
		"TX_COORDINATION_MODE": "mongo-lease",
		"PERSISTENCE_MODE":     "memory",
	})
	if err == nil {
		t.Fatal("expected an error for mongo-lease coordination with memory persistence")
	}
}

// TestLoadMongoLeaseAcceptedWithMongoPersistence is the positive case.
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

// TestLoadDefaultsAreDevelopment pins the safe-for-local-dev defaults: no
// ENVIRONMENT/PERSISTENCE_MODE set at all must not accidentally trip the
// production-only checks below.
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

// TestLoadDevelopmentAllowsEmptyWebhookSecret documents the intended
// non-production behavior: an unset secret is allowed (the feature is
// disabled by cmd/platform's buildApp, not by config.Load).
func TestLoadDevelopmentAllowsEmptyWebhookSecret(t *testing.T) {
	if _, err := LoadFromMap(map[string]string{"ENVIRONMENT": "development"}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadRejectsWeakWebhookSecretAnyEnvironment: the entropy floor
// applies whenever a secret is configured at all, not just in production —
// a placeholder like "changeme" should never silently pass.
func TestLoadRejectsWeakWebhookSecretAnyEnvironment(t *testing.T) {
	_, err := LoadFromMap(map[string]string{"KYC_WEBHOOK_HMAC_SECRET": "too-short"})
	if err == nil {
		t.Fatal("expected error for a below-minimum-length webhook secret")
	}
}

// TestLoadProductionRequiresWebhookSecret checks that starting the
// production configuration with an empty secret fails at startup.
func TestLoadProductionRequiresWebhookSecret(t *testing.T) {
	_, err := LoadFromMap(map[string]string{"ENVIRONMENT": "production"})
	if err == nil {
		t.Fatal("expected production startup to fail with no KYC_WEBHOOK_HMAC_SECRET configured")
	}
}

// TestLoadProductionAcceptsStrongWebhookSecret is the positive counterpart:
// production with a secret meeting the documented minimum must start.
// prodEnv is a minimal production config that PASSES every fail-closed
// check, so a test can drop one required field and confirm it (and only it)
// makes Load fail.
func prodEnv() map[string]string {
	return map[string]string{
		"ENVIRONMENT":             "production",
		"KYC_WEBHOOK_HMAC_SECRET": "01234567890123456789012345678901", // 33 bytes
		"ADMIN_ADDRESS":           "0x00000000000000000000000000000000000000A1",
		"JWT_SECRET":              "jwt-secret-0000000000000000000000000000000",
		"PROJECT_ID":              "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		// IPFS_API_URL defaults to a non-empty value, so production's
		// "at least one independent backup destination" requirement (see
		// Load's IPFSReplicationThreshold checks) always applies unless
		// explicitly opted out — a fixed path is enough here since
		// config.Load never touches the filesystem itself.
		"IPFS_BACKUP_ARCHIVE_DIR": "./testdata/ipfs-archive",
		// Production requires at least one fee cap set.
		"TX_MAX_FEE_PER_GAS_WEI": "500000000000",
	}
}

func TestLoadProductionAcceptsStrongWebhookSecret(t *testing.T) {
	if _, err := LoadFromMap(prodEnv()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadProductionRequiresAdminAuth is the admin-auth regression test:
// admin routes authenticate a wallet-signature JWT, so production requires a
// configured admin wallet address AND a JWT signing secret; either missing is
// a startup failure.
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

// TestLoadProductionRejectsMockAndRawKeyProviders is the regression test
// for unsafe hot-key providers.
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

// TestLoadProductionRejectsMemoryPersistence: production must never
// silently run on volatile in-memory repositories.
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

// TestLoadProductionRequiresProjectID: production must not start without a
// pinned projectId — the gate that fixes which Asset Profile the deployment
// will accept has to be armed before any profile can be created.
func TestLoadProductionRequiresProjectID(t *testing.T) {
	env := prodEnv()
	delete(env, "PROJECT_ID")
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with no PROJECT_ID set")
	}
}

// TestLoadRejectsMalformedProjectID: a non-UUID projectId is caught at load in
// any environment, not deferred to the first profile create.
func TestLoadRejectsMalformedProjectID(t *testing.T) {
	env := map[string]string{"PROJECT_ID": "not-a-uuid"}
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected load to reject a malformed PROJECT_ID")
	}
}

// TestLoadProductionRequiresIPFSBackupDestination: IPFS_API_URL defaults
// to a non-empty value, so
// production must refuse to start without at least one independent
// replication destination configured, rather than silently falling back to
// a single bare local Kubo node with no durable-publication tracking.
func TestLoadProductionRequiresIPFSBackupDestination(t *testing.T) {
	env := prodEnv()
	delete(env, "IPFS_BACKUP_ARCHIVE_DIR")
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to fail with IPFS_API_URL enabled but no backup destination configured")
	}
}

// TestLoadProductionRejectsReplicationThresholdOutOfRange covers both
// directions of "validate 1 <= threshold <= destination count."
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

// TestLoadProductionAcceptsReplicationThresholdWithinRange is the positive
// case, including a second backup destination raising the valid ceiling.
func TestLoadProductionAcceptsReplicationThresholdWithinRange(t *testing.T) {
	env := prodEnv()
	env["IPFS_BACKUP_KUBO_URL"] = "http://backup-kubo:5001"
	env["IPFS_REPLICATION_THRESHOLD"] = "2"
	if _, err := LoadFromMap(env); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadDevelopmentAllowsMemoryPersistence: memory is a supported,
// explicit opt-in outside production (local smoke tests, CI).
func TestLoadDevelopmentAllowsMemoryPersistence(t *testing.T) {
	if _, err := LoadFromMap(map[string]string{"PERSISTENCE_MODE": "memory"}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadDefaultMetricsAddrIsLoopback: metrics must not listen on all
// interfaces by default.
func TestLoadDefaultMetricsAddrIsLoopback(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MetricsAddr != "127.0.0.1:9090" {
		t.Errorf("MetricsAddr = %q, want 127.0.0.1:9090", cfg.MetricsAddr)
	}
}

// TestLoadDefaultHTTPHardening: every listener gets non-zero timeouts and a
// header-size cap out of the box.
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

// TestLoadFeeCapsMustBeNonNegativeIntegers is the fee-cap config-validation
// regression test.
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

// TestLoadProductionRejectsTrivialSecrets is a table-driven regression test:
// every known placeholder, and every too-short value, must be rejected for
// the JWT signing secret.
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

// TestLoadProductionAcceptsStrongSecrets is the positive counterpart,
// already implicitly covered by prodEnv()-based tests, made explicit here.
func TestLoadProductionAcceptsStrongSecrets(t *testing.T) {
	if _, err := LoadFromMap(prodEnv()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoadProductionRejectsNonPositiveDurationsAndLimits is a table-driven
// test that positive lower/upper bounds are enforced for every security
// duration, body/header limit, rate/burst, and confirmation count. Each
// entry sets exactly one previously-unbounded knob to a disabling/invalid
// value on top of an otherwise-valid prodEnv() and expects Load to reject
// it.
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

// TestLoadProductionDisabledControlsRequireExplicitOverride covers the
// three genuinely "disabled" controls (body limit, rate limit, fee cap):
// rejected by default, accepted only with their narrowly-named
// PRODUCTION_ALLOW_* override set.
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

// TestLoadProductionRejectsZeroBurstWhenRateLimitEnabled is the specific
// cross-field constraint: a positive RPS with a non-positive burst is
// still a broken (self-inflicted-DoS-prone) configuration.
func TestLoadProductionRejectsZeroBurstWhenRateLimitEnabled(t *testing.T) {
	env := prodEnv()
	env["RATE_LIMIT_RPS"] = "50"
	env["RATE_LIMIT_BURST"] = "0"
	if _, err := LoadFromMap(env); err == nil {
		t.Fatal("expected production startup to reject RATE_LIMIT_BURST=0 with RATE_LIMIT_RPS enabled")
	}
}

// TestLoadProductionRejectsOverflowAndGarbageDurations proves malformed
// duration/int inputs (not just semantically zero/negative ones) are
// caught by the underlying parse step for every newly-bounded field, not
// only the pre-existing ones already covered by TestLoadInvalid.
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
