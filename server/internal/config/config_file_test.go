package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes contents to a temp YAML file and returns its path
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

// TestLoadExampleConfig requires server/config.example.yaml to match fileSchema;
// LoadFile rejects unknown keys
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

// TestLoadFileMissing checks that a nonexistent path is an error, not a fall-back to defaults
func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

// TestLoadFileEmptyYieldsDefaults checks that an empty YAML document resolves to
// the defaults, as LoadFromMap with an empty map does
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

// TestLoadFileFullRoundTrip checks YAML strings, numbers, durations, comma-joined
// trusted proxies and secrets across every field group
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
	// Keep cors_allowed_origins under http:; strict decoding rejects it in another block
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

// TestLoadFileRejectsUnknownKey checks that a misspelled key fails instead of
// silently taking a default
func TestLoadFileRejectsUnknownKey(t *testing.T) {
	const yaml = `
security:
  jwt_secrets: "typo-on-the-key-name"
`
	if _, err := LoadFile(writeConfig(t, yaml)); err == nil {
		t.Fatal("expected an error for an unknown YAML key")
	}
}

// TestLoadFileRejectsMalformedYAML checks that a malformed document is a parse
// error, not a load of defaults
func TestLoadFileRejectsMalformedYAML(t *testing.T) {
	if _, err := LoadFile(writeConfig(t, "http: [unterminated")); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

// TestLoadFileRejectsWrongScalarType checks that a string where an int64 is
// expected is a decode error
func TestLoadFileRejectsWrongScalarType(t *testing.T) {
	if _, err := LoadFile(writeConfig(t, "chain:\n  id: \"not-a-number\"\n")); err == nil {
		t.Fatal("expected an error for a non-integer chain.id")
	}
}

// prodYAML supplies minimal valid production settings for LoadFile, matching
// config_test.go's prodEnv
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

// TestLoadFileProductionRoundTrips checks that a valid production YAML file
// loads
func TestLoadFileProductionRoundTrips(t *testing.T) {
	cfg, err := LoadFile(writeConfig(t, prodYAML))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if cfg.Environment != EnvProduction {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
}

// TestLoadFileProductionRejectsPlaceholderWebhookSecret rejects 32 zeroes despite
// meeting the length floor, applying the shared JWT/webhook placeholder blocklist
// against forgeable KYC secrets
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

// TestLoadFileProductionValidationFires weakens one required value per case to check
// that YAML enforces the same production rules as LoadFromMap
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

// TestLoadFileProductionExplicitZeroHonored checks that explicit zero reaches
// production validation and is rejected where it disables a control
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

// TestLoadFileProductionOverride checks YAML production_overrides: disabled rate
// limiting requires explicit permission
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

// TestExampleConfigParses checks that server/config.example.yaml parses with no
// unknown keys and well-typed values, and loads
func TestExampleConfigParses(t *testing.T) {
	if _, err := LoadFile("../../config.example.yaml"); err != nil {
		t.Fatalf("config.example.yaml failed to load: %v", err)
	}
}

// TestLoadFileComplianceGuards covers the holder cap, sanctions list and monitoring action
func TestLoadFileComplianceGuards(t *testing.T) {
	cfg, err := LoadFile(writeConfig(t, "compliance:\n  max_allowed_wallets: 99\n  sanctions_list_file: /etc/rwa/sdn.txt\nkyc:\n  monitoring_action: block\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxAllowedWallets != 99 || cfg.SanctionsListFile != "/etc/rwa/sdn.txt" || cfg.KYCMonitoringAction != "block" {
		t.Errorf("got %d %q %q", cfg.MaxAllowedWallets, cfg.SanctionsListFile, cfg.KYCMonitoringAction)
	}

	def, err := LoadFromMap(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if def.MaxAllowedWallets != 0 || def.SanctionsListFile != "" || def.KYCMonitoringAction != "alert" {
		t.Errorf("defaults: %d %q %q", def.MaxAllowedWallets, def.SanctionsListFile, def.KYCMonitoringAction)
	}

	env, err := LoadFromMap(map[string]string{"MAX_ALLOWED_WALLETS": "5", "KYC_MONITORING_ACTION": "block"})
	if err != nil || env.MaxAllowedWallets != 5 || env.KYCMonitoringAction != "block" {
		t.Errorf("env override: %+v %v", env.MaxAllowedWallets, err)
	}

	for key, val := range map[string]string{"MAX_ALLOWED_WALLETS": "-1", "KYC_MONITORING_ACTION": "freeze"} {
		if _, err := LoadFromMap(map[string]string{key: val}); err == nil {
			t.Errorf("%s=%s must be rejected", key, val)
		}
	}
}
