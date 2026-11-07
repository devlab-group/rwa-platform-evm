package keeper

import (
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"gopkg.in/yaml.v3"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/keys"
)

const defaultPollInterval = 30 * time.Second

// Config defines a separate keeper YAML file that needs neither the platform's compliance
// key nor its JWT secret
type Config struct {
	Environment       string        `yaml:"environment"`
	RPCURL            string        `yaml:"rpc_url"`
	ChainID           int64         `yaml:"chain_id"`
	VaultAddress      string        `yaml:"vault_address"`
	PollInterval      time.Duration `yaml:"poll_interval"`
	MaxFeePerGasWei   string        `yaml:"max_fee_per_gas_wei"`
	MaxTipPerGasWei   string        `yaml:"max_tip_per_gas_wei"`
	MaxTxTotalCostWei string        `yaml:"max_tx_total_cost_wei"`
	Keys              struct {
		ProviderMode         string `yaml:"provider_mode"`
		KeeperKey            string `yaml:"keeper_key"`
		KeystoreDir          string `yaml:"keystore_dir"`
		KeystorePasswordFile string `yaml:"keystore_password_file"`
		VaultAddr            string `yaml:"vault_addr"`
		VaultToken           string `yaml:"vault_token"`
		VaultKeyPrefix       string `yaml:"vault_key_prefix"`
		KMSMockSeed          string `yaml:"kms_mock_seed"`
	} `yaml:"keys"`

	// FeeCaps holds the parsed fee limits
	FeeCaps blockchain.FeeCaps `yaml:"-"`
}

// LoadConfig reads and validates path. Unknown keys are errors, as in the platform config
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()

	cfg := Config{Environment: "development", PollInterval: defaultPollInterval}
	cfg.Keys.VaultKeyPrefix = "rwa-"
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Environment != "development" && c.Environment != "production" {
		return fmt.Errorf("environment: want development or production, got %q", c.Environment)
	}
	if c.RPCURL == "" {
		return fmt.Errorf("rpc_url is required")
	}
	if c.ChainID <= 0 {
		return fmt.Errorf("chain_id is required")
	}
	if !common.IsHexAddress(c.VaultAddress) || common.HexToAddress(c.VaultAddress) == (common.Address{}) {
		return fmt.Errorf("vault_address: want a non-zero address, got %q", c.VaultAddress)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll_interval must be positive, got %s", c.PollInterval)
	}

	var err error
	if c.FeeCaps.MaxFeePerGas, err = parseWei("max_fee_per_gas_wei", c.MaxFeePerGasWei); err != nil {
		return err
	}
	if c.FeeCaps.MaxTipPerGas, err = parseWei("max_tip_per_gas_wei", c.MaxTipPerGasWei); err != nil {
		return err
	}
	if c.FeeCaps.MaxTotalCost, err = parseWei("max_tx_total_cost_wei", c.MaxTxTotalCostWei); err != nil {
		return err
	}

	if c.Environment == "production" {
		mode := keys.Mode(c.Keys.ProviderMode)
		if mode == "" || mode == keys.ModeRaw || mode == keys.ModeKMSMock {
			return fmt.Errorf("keys.provider_mode: production needs %q or %q, got %q", keys.ModeLocalKeystore, keys.ModeVault, mode)
		}
		if c.FeeCaps.MaxFeePerGas == nil && c.FeeCaps.MaxTotalCost == nil {
			return fmt.Errorf("production needs max_fee_per_gas_wei or max_tx_total_cost_wei")
		}
	}
	return nil
}

// KeyConfig maps the keys section to the key loader's settings
func (c Config) KeyConfig() keys.Config {
	return keys.Config{
		Mode:           keys.Mode(c.Keys.ProviderMode),
		RawHexKey:      c.Keys.KeeperKey,
		KeystoreDir:    c.Keys.KeystoreDir,
		PasswordFile:   c.Keys.KeystorePasswordFile,
		VaultAddr:      c.Keys.VaultAddr,
		VaultToken:     c.Keys.VaultToken,
		VaultKeyPrefix: c.Keys.VaultKeyPrefix,
		KMSMockSeedHex: c.Keys.KMSMockSeed,
	}
}

// parseWei rejects bad values instead of treating them as "no cap"
func parseWei(name, s string) (*big.Int, error) {
	if s == "" {
		return nil, nil
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() <= 0 {
		return nil, fmt.Errorf("%s: want a positive base-10 wei amount, got %q", name, s)
	}
	return n, nil
}
