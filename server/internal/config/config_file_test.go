package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes contents to a temp YAML file and returns its path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

// TestLoadExampleConfig: the committed server/config.example.yaml must load
// cleanly. Because LoadFile uses KnownFields(true), this fails if the example
// documents a key the fileSchema doesn't declare (or vice versa) — a cheap
// guard against the example and the schema drifting apart.
func TestLoadExampleConfig(t *testing.T) {
	cfg, err := LoadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("LoadFile(config.example.yaml) error = %v", err)
	}
	if cfg.KYCProvider != "none" {
		t.Errorf("KYCProvider = %q, want none (example default)", cfg.KYCProvider)
	}
	if cfg.KYCOnfidoRegion != "eu" {
		t.Errorf("KYCOnfidoRegion = %q, want eu (example default)", cfg.KYCOnfidoRegion)
	}
}

// TestLoadFileMissing: a nonexistent path is a startup error, not a silent
// fall-through to defaults.
func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

// TestLoadFileEmptyYieldsDefaults: an empty YAML document resolves exactly
// the documented defaults, same as LoadFromMap({}).
func TestLoadFileEmptyYieldsDefaults(t *testing.T) {
	cfg, err := LoadFile(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
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
	if cfg.Environment != EnvDevelopment {
		t.Errorf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.PersistenceMode != "mongo" {
		t.Errorf("PersistenceMode = %q, want mongo", cfg.PersistenceMode)
	}
	if cfg.IdempotencyTTL != 24*time.Hour {
		t.Errorf("IdempotencyTTL = %v, want 24h", cfg.IdempotencyTTL)
	}
}

// TestLoadFileFullRoundTrip parses a complete YAML — INCLUDING secrets — and
// asserts every field group flattens and resolves correctly: strings, typed
// numbers, durations, the comma-joined trusted proxies list, and secrets.
func TestLoadFileFullRoundTrip(t *testing.T) {
	const yaml = `
environment: development
http:
  addr: ":9090"
  metrics_addr: "0.0.0.0:9100"
  read_timeout: "45s"
  max_header_bytes: 16384
  max_request_body_bytes: 1048576
  trusted_proxies:
    - "10.0.0.1"
    - "172.16.0.0/12"
  cors_allowed_origins:
    - "http://localhost:5173"
    - "https://investor.example"
chain:
  rpc_url: "http://chain:8545"
  id: 5
  confirmations: 12
  fee_mode: "legacy"
  max_fee_per_gas_wei: "500000000000"
contract:
  factory_address: "0xFac70"
  start_block: 12345
  project_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
mongo:
  uri: "mongodb://mongo:27017"
  db: "custom_db"
ipfs:
  api_url: "http://ipfs:5001"
  backup_kubo_url: "http://ipfs2:5001"
  replication_threshold: 1
security:
  kyc_webhook_hmac_secret: "0123456789012345678901234567890123"
  idempotency_ttl: "1h"
  wallet_session_ttl: "10m"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
  rate_limit_rps: 25.5
  rate_limit_burst: 40
keys:
  provider_mode: "raw"
  compliance_key: "0xdeadbeef"
tx:
  coordination_mode: "in-process"
  lease_ttl: "45s"
alerts:
  pending_redemption_sla: "72h"
`
	cfg, err := LoadFile(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTPAddr", cfg.HTTPAddr, ":9090"},
		{"MetricsAddr", cfg.MetricsAddr, "0.0.0.0:9100"},
		{"HTTPReadTimeout", cfg.HTTPReadTimeout, 45 * time.Second},
		{"HTTPMaxHeaderBytes", cfg.HTTPMaxHeaderBytes, 16384},
		{"MaxRequestBodyBytes", cfg.MaxRequestBodyBytes, int64(1048576)},
		{"ChainRPCURL", cfg.ChainRPCURL, "http://chain:8545"},
		{"ChainID", cfg.ChainID, int64(5)},
		{"Confirmations", cfg.Confirmations, uint64(12)},
		{"FeeMode", cfg.FeeMode, "legacy"},
		{"MaxFeePerGasWei", cfg.MaxFeePerGasWei, "500000000000"},
		{"FactoryAddress", cfg.FactoryAddress, "0xFac70"},
		{"StartBlock", cfg.StartBlock, uint64(12345)},
		{"ProjectID", cfg.ProjectID, "3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{"MongoURI", cfg.MongoURI, "mongodb://mongo:27017"},
		{"MongoDB", cfg.MongoDB, "custom_db"},
		{"IPFSAPIURL", cfg.IPFSAPIURL, "http://ipfs:5001"},
		{"IPFSBackupKuboURL", cfg.IPFSBackupKuboURL, "http://ipfs2:5001"},
		{"IPFSReplicationThreshold", cfg.IPFSReplicationThreshold, 1},
		{"KYCWebhookHMACSecret", cfg.KYCWebhookHMACSecret, "0123456789012345678901234567890123"},
		{"IdempotencyTTL", cfg.IdempotencyTTL, time.Hour},
		{"WalletSessionTTL", cfg.WalletSessionTTL, 10 * time.Minute},
		{"AdminAddress", cfg.AdminAddress, "0x00000000000000000000000000000000000000A1"},
		{"JWTSecret", cfg.JWTSecret, "jwt-secret-0000000000000000000000000000000"},
		{"RateLimitRPS", cfg.RateLimitRPS, 25.5},
		{"RateLimitBurst", cfg.RateLimitBurst, 40},
		{"ComplianceKeyHex", cfg.ComplianceKeyHex, "0xdeadbeef"},
		{"TxLeaseTTL", cfg.TxLeaseTTL, 45 * time.Second},
		{"PendingRedemptionSLA", cfg.PendingRedemptionSLA, 72 * time.Hour},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
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
	// cors_allowed_origins lives under http:, alongside trusted_proxies — it
	// is an HTTP-transport concern, not a credential/identity one. Asserting
	// it through the FILE path (not just the env map) is what pins the block
	// it belongs to: a key under the wrong block is rejected outright by
	// KnownFields, so this would fail rather than silently ignore it.
	wantOrigins := []string{"http://localhost:5173", "https://investor.example"}
	if len(cfg.CORSAllowedOrigins) != len(wantOrigins) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, wantOrigins)
	}
	for i, w := range wantOrigins {
		if cfg.CORSAllowedOrigins[i] != w {
			t.Errorf("CORSAllowedOrigins[%d] = %q, want %q", i, cfg.CORSAllowedOrigins[i], w)
		}
	}
}

// TestLoadFileRejectsUnknownKey: a typo in a security-relevant key must fail
// startup (KnownFields(true)) rather than silently taking a default.
func TestLoadFileRejectsUnknownKey(t *testing.T) {
	const yaml = `
security:
  jwt_secrets: "typo-on-the-key-name"
`
	if _, err := LoadFile(writeConfig(t, yaml)); err == nil {
		t.Fatal("expected an error for an unknown YAML key")
	}
}

// TestLoadFileRejectsMalformedYAML: a syntactically broken document is a
// parse error, not a silent empty-defaults load.
func TestLoadFileRejectsMalformedYAML(t *testing.T) {
	if _, err := LoadFile(writeConfig(t, "http: [unterminated")); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

// TestLoadFileRejectsWrongScalarType: a value of the wrong type (a string
// where an int64 is expected) is a decode error.
func TestLoadFileRejectsWrongScalarType(t *testing.T) {
	if _, err := LoadFile(writeConfig(t, "chain:\n  id: \"not-a-number\"\n")); err == nil {
		t.Fatal("expected an error for a non-integer chain.id")
	}
}

// prodYAML is a minimal production config that passes every fail-closed
// check — the LoadFile counterpart of config_test.go's prodEnv().
const prodYAML = `
environment: production
contract:
  project_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`

// TestLoadFileProductionRoundTrips: a valid production YAML (secrets included)
// starts, proving the production fail-closed checks run through LoadFile.
func TestLoadFileProductionRoundTrips(t *testing.T) {
	cfg, err := LoadFile(writeConfig(t, prodYAML))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if cfg.Environment != EnvProduction {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
}

// TestLoadFileProductionRejectsPlaceholderWebhookSecret: the 32-zero string is
// a known placeholder AND exactly 32 bytes, so the length floor alone waves it
// through. It must be caught by the same blocklist that protects JWT_SECRET —
// a forged KYC decision under a guessable webhook secret is relayed on-chain by
// the compliance hot key.
func TestLoadFileProductionRejectsPlaceholderWebhookSecret(t *testing.T) {
	yaml := strings.Replace(prodYAML,
		`kyc_webhook_hmac_secret: "01234567890123456789012345678901"`,
		`kyc_webhook_hmac_secret: "00000000000000000000000000000000"`, 1)
	_, err := LoadFile(writeConfig(t, yaml))
	if err == nil {
		t.Fatal("expected production startup to fail for a placeholder webhook secret")
	}
	if !strings.Contains(err.Error(), "KYC_WEBHOOK_HMAC_SECRET") {
		t.Fatalf("error = %v, want it to name KYC_WEBHOOK_HMAC_SECRET", err)
	}
}

// TestLoadFileProductionValidationFires: the SAME production checks LoadFromMap
// enforces must also fire when the config arrives as YAML. Each case drops or
// weakens exactly one required value and expects startup to fail.
func TestLoadFileProductionValidationFires(t *testing.T) {
	cases := map[string]string{
		"weak webhook secret": `
environment: production
security:
  kyc_webhook_hmac_secret: "too-short"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"missing jwt secret": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"missing admin address": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"placeholder jwt secret": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "changeme"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"malformed admin address": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "not-an-address"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"memory persistence": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
mongo:
  persistence_mode: "memory"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"raw key provider with hot key": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
keys:
  provider_mode: "raw"
  compliance_key: "0x11"
`,
		"no ipfs backup destination": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
chain:
  max_fee_per_gas_wei: "500000000000"
`,
		"unbounded fees without override": `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFile(writeConfig(t, yaml)); err == nil {
				t.Fatalf("expected production startup to fail for %q", name)
			}
		})
	}
}

// TestLoadFileProductionExplicitZeroHonored proves an explicit numeric zero
// (distinct from an omitted key) is carried through the pointer flattening
// and rejected in production where it would disable a control.
func TestLoadFileProductionExplicitZeroHonored(t *testing.T) {
	const yaml = `
environment: production
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
  confirmations: 0
`
	if _, err := LoadFile(writeConfig(t, yaml)); err == nil {
		t.Fatal("expected production startup to reject chain.confirmations: 0")
	}
}

// TestLoadFileProductionOverride proves the PRODUCTION_ALLOW_* escape hatches
// are reachable from YAML: disabling the rate limit is rejected by default
// but accepted with the explicit override.
func TestLoadFileProductionOverride(t *testing.T) {
	base := `
environment: production
contract:
  project_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
security:
  kyc_webhook_hmac_secret: "01234567890123456789012345678901"
  admin_address: "0x00000000000000000000000000000000000000A1"
  jwt_secret: "jwt-secret-0000000000000000000000000000000"
  rate_limit_rps: 0
ipfs:
  backup_archive_dir: "./testdata/ipfs-archive"
chain:
  max_fee_per_gas_wei: "500000000000"
`
	if _, err := LoadFile(writeConfig(t, base)); err == nil {
		t.Fatal("expected rate_limit_rps: 0 to be rejected without an override")
	}
	withOverride := base + `
production_overrides:
  allow_disabled_rate_limit: true
`
	if _, err := LoadFile(writeConfig(t, withOverride)); err != nil {
		t.Fatalf("expected the explicit override to be accepted: %v", err)
	}
}

// TestExampleConfigParses keeps server/config.example.yaml honest: the shipped
// example must always parse (no unknown keys, well-typed values) and load.
func TestExampleConfigParses(t *testing.T) {
	if _, err := LoadFile("../../config.example.yaml"); err != nil {
		t.Fatalf("config.example.yaml failed to load: %v", err)
	}
}
