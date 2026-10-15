// Package keys provides blockchain.Signer-compatible hot-key backends selected by
// keys.provider_mode:
//   - "raw" (default): plaintext config key; loading warns, and production rejects a
//     configured compliance key.
//   - "local-keystore": per-role encrypted go-ethereum JSON, decrypted into process
//     memory at Load/Reload.
//   - "vault": remote signing; see vaultProvider for secp256k1 requirements, rotation
//     failures and role migration.
//   - "kms-mock": seed-derived CI/local keys without a KMS account; loading warns and
//     production rejects it.
//
// Provider.Reload refreshes backend state, but the server does not call it. Restart to
// load changed keystore/Vault keys.
package keys

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Mode selects which backend Load uses
type Mode string

const (
	ModeRaw            Mode = "raw"
	ModeLocalKeystore  Mode = "local-keystore"
	ModeVault          Mode = "vault"
	ModeKMSMock        Mode = "kms-mock"
	defaultModeIfEmpty      = ModeRaw
)

// Provider extends blockchain.Signer structurally with Reload/Close without importing
// blockchain, supporting address lookup, transaction signing and key-source refresh
type Provider interface {
	Address(ctx context.Context) (common.Address, error)
	SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
	// Reload re-reads or re-fetches key material from the Provider's source.
	// It is safe to call concurrently with SignTx
	Reload(ctx context.Context) error
	// Close best-effort zeroes private scalars in raw, local-keystore and kms-mock; Vault
	// holds no private key and does nothing. Call once at shutdown. Go runtime copies may
	// survive the wipe.
	Close() error
}

// zeroECDSAKey overwrites the scalar's mutable Bits() before SetInt64(0). SetInt64
// alone may leave secret words in the backing allocation, recoverable from dumps or
// swap. Raw, local-keystore and kms-mock use it.
func zeroECDSAKey(priv *ecdsa.PrivateKey) {
	if priv == nil || priv.D == nil {
		return
	}
	words := priv.D.Bits()
	for i := range words {
		words[i] = 0
	}
	priv.D.SetInt64(0)
}

// zeroBytes overwrites retained raw key bytes alongside the parsed private key
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Config bundles every backend's settings. Only the fields for the selected
// Mode are read.
type Config struct {
	Mode Mode

	// RawHexKey is the "raw" mode hex private key, with or without a 0x prefix

	RawHexKey string

	// "local-keystore" mode decrypts KeystoreDir/<role>.json with the contents of
	// PasswordFile, trailing CR and LF characters removed
	KeystoreDir  string
	PasswordFile string

	// Vault roles use VaultKeyPrefix+role, such as rwa-compliance. Scope VaultToken to
	// read transit/keys/<name> and update transit/sign/<name>. The backend must support
	// the secp256k1 Transit protocol described by vaultProvider.
	VaultAddr      string
	VaultToken     string
	VaultKeyPrefix string

	// KMSMockSeedHex derives keys as keccak256(literal seed string || role name), without
	// hex-decoding the seed. Empty seeds generate and log a process-specific address that
	// changes on restart.
	KMSMockSeedHex string
}

// Load resolves role through cfg.Mode, defaulting to raw. Empty RawHexKey returns (nil,
// nil), leaving the role unavailable rather than failing startup.
func Load(role string, cfg Config) (Provider, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = defaultModeIfEmpty
	}
	switch mode {
	case ModeRaw:
		if cfg.RawHexKey == "" {
			return nil, nil
		}
		// Warn when a configured raw key uses the default backend
		log.Printf("keys: WARNING - role %q is using raw env-hex key provider. NOT for production; use local-keystore or vault (set KEY_PROVIDER_MODE).", role)
		return newRawKeyProvider(cfg.RawHexKey)
	case ModeLocalKeystore:
		return newLocalKeystoreProvider(role, cfg.KeystoreDir, cfg.PasswordFile)
	case ModeVault:
		return newVaultProvider(role, cfg.VaultAddr, cfg.VaultToken, cfg.VaultKeyPrefix)
	case ModeKMSMock:
		return newKMSMockProvider(role, cfg.KMSMockSeedHex)
	default:
		return nil, fmt.Errorf("keys: unknown KEY_PROVIDER_MODE %q (want %q, %q, %q, or %q)",
			mode, ModeRaw, ModeLocalKeystore, ModeVault, ModeKMSMock)
	}
}
