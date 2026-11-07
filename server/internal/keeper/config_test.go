package keeper

import (
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/keys"
)

const validBase = `
rpc_url: "http://127.0.0.1:8545"
chain_id: 31337
vault_address: "0x00000000000000000000000000000000000000aa"
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keeper.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig_Minimal_AppliesDefaults(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, validBase))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, want 30s", cfg.PollInterval)
	}
	if cfg.Keys.VaultKeyPrefix != "rwa-" {
		t.Errorf("VaultKeyPrefix = %q, want rwa-", cfg.Keys.VaultKeyPrefix)
	}
	if cfg.FeeCaps.MaxFeePerGas != nil || cfg.FeeCaps.MaxTipPerGas != nil || cfg.FeeCaps.MaxTotalCost != nil {
		t.Errorf("FeeCaps = %+v, want none", cfg.FeeCaps)
	}
}

func TestLoadConfig_FullFile_ParsesEveryField(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `
environment: production
rpc_url: "https://rpc.example"
chain_id: 8453
vault_address: "0x00000000000000000000000000000000000000bb"
poll_interval: "10s"
max_fee_per_gas_wei: "100"
max_tip_per_gas_wei: "200"
max_tx_total_cost_wei: "300"
keys:
  provider_mode: "local-keystore"
  keeper_key: "k"
  keystore_dir: "/d"
  keystore_password_file: "/p"
  vault_addr: "https://vault"
  vault_token: "t"
  vault_key_prefix: "x-"
  kms_mock_seed: "s"
`))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.RPCURL != "https://rpc.example" || cfg.ChainID != 8453 || cfg.PollInterval != 10*time.Second ||
		cfg.VaultAddress != "0x00000000000000000000000000000000000000bb" {
		t.Errorf("parsed %+v", cfg)
	}
	for name, got := range map[string]struct {
		value *big.Int
		want  int64
	}{
		"fee":   {cfg.FeeCaps.MaxFeePerGas, 100},
		"tip":   {cfg.FeeCaps.MaxTipPerGas, 200},
		"total": {cfg.FeeCaps.MaxTotalCost, 300},
	} {
		if got.value == nil || got.value.Int64() != got.want {
			t.Errorf("%s cap = %v, want %d", name, got.value, got.want)
		}
	}
	want := keys.Config{
		Mode:           keys.ModeLocalKeystore,
		RawHexKey:      "k",
		KeystoreDir:    "/d",
		PasswordFile:   "/p",
		VaultAddr:      "https://vault",
		VaultToken:     "t",
		VaultKeyPrefix: "x-",
		KMSMockSeedHex: "s",
	}
	if got := cfg.KeyConfig(); got != want {
		t.Errorf("KeyConfig = %+v, want %+v", got, want)
	}
}

func TestLoadConfig_InvalidFiles_AreRejected(t *testing.T) {
	const prod = "environment: production\n" + validBase
	const cap = "max_fee_per_gas_wei: \"1\"\n"
	for name, tc := range map[string]struct{ body, want string }{
		"unknown key":          {validBase + "compliance_key: \"x\"\n", "compliance_key"},
		"unknown environment":  {"environment: prod\n" + validBase, "environment"},
		"missing rpc_url":      {"chain_id: 1\nvault_address: \"0x00000000000000000000000000000000000000aa\"\n", "rpc_url"},
		"missing chain_id":     {"rpc_url: \"x\"\nvault_address: \"0x00000000000000000000000000000000000000aa\"\n", "chain_id"},
		"malformed vault":      {"rpc_url: \"x\"\nchain_id: 1\nvault_address: \"0x12\"\n", "vault_address"},
		"zero vault":           {"rpc_url: \"x\"\nchain_id: 1\nvault_address: \"0x0000000000000000000000000000000000000000\"\n", "vault_address"},
		"zero interval":        {validBase + "poll_interval: \"0s\"\n", "poll_interval"},
		"negative interval":    {validBase + "poll_interval: \"-1s\"\n", "poll_interval"},
		"non-numeric fee cap":  {validBase + "max_fee_per_gas_wei: \"1gwei\"\n", "max_fee_per_gas_wei"},
		"zero tip cap":         {validBase + "max_tip_per_gas_wei: \"0\"\n", "max_tip_per_gas_wei"},
		"negative total cap":   {validBase + "max_tx_total_cost_wei: \"-1\"\n", "max_tx_total_cost_wei"},
		"production empty key": {prod + cap, "provider_mode"},
		"production raw key":   {prod + cap + "keys:\n  provider_mode: \"raw\"\n", "provider_mode"},
		"production kms-mock":  {prod + cap + "keys:\n  provider_mode: \"kms-mock\"\n", "provider_mode"},
		"production no cap":    {prod + "keys:\n  provider_mode: \"vault\"\n", "max_fee_per_gas_wei"},
		"production tip only":  {prod + "max_tip_per_gas_wei: \"1\"\nkeys:\n  provider_mode: \"vault\"\n", "max_fee_per_gas_wei"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("LoadConfig accepted the file")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestLoadConfig_Production_AcceptsManagedKeysWithEitherCap(t *testing.T) {
	for _, tail := range []string{
		"max_fee_per_gas_wei: \"1\"\nkeys:\n  provider_mode: \"local-keystore\"\n",
		"max_tx_total_cost_wei: \"1\"\nkeys:\n  provider_mode: \"vault\"\n",
	} {
		if _, err := LoadConfig(writeConfig(t, "environment: production\n"+validBase+tail)); err != nil {
			t.Errorf("%q: %v", tail, err)
		}
	}
}

func TestLoadConfig_MissingFile_IsAnError(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml")); !os.IsNotExist(err) {
		t.Fatalf("error = %v, want not-exist", err)
	}
}
