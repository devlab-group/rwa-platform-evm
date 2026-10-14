// Package keys implements the server's hot-key providers. A Provider reports
// an address and signs transactions, matching internal/blockchain.Signer
// structurally. Load resolves a role to one of four backends selected by
// keys.provider_mode:
//
//   - "raw": a plaintext hex private key from configuration. This is the
//     default when the mode is unset, and the least secure option, so Load logs
//     a warning whenever it is active. Production config validation rejects it
//     when a compliance key is set.
//   - "local-keystore": a password-encrypted go-ethereum keystore JSON file
//     per role. The key is decrypted into memory at Load and on Reload, so it
//     is still a hot key on this host.
//   - "vault": a HashiCorp Vault Transit-compatible backend signs over HTTP, so
//     this server never holds the private key. Signing always asks for the
//     latest key version, but the address is cached at Load and Reload, so
//     after a rotation on the Vault side signing fails until the process
//     restarts. See vaultProvider.
//   - "kms-mock": a non-production stand-in for a real KMS, with deterministic
//     per-role keys derived from a seed. It lets CI and local development
//     exercise the Provider abstraction without a KMS account. Load logs a
//     warning when it is active, and config validation rejects it in
//     production.
//
// Provider.Reload re-reads key material from its source, but no server code
// calls it, so a changed keystore file or Vault key takes effect after a
// restart.
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

// Provider is a hot key that reports its address, signs transactions, and
// reloads its key material from its source. It extends
// internal/blockchain.Signer with Reload and Close, so a Provider can be used
// as a Signer without this package importing internal/blockchain.
type Provider interface {
	Address(ctx context.Context) (common.Address, error)
	SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
	// Reload re-reads or re-fetches key material from the Provider's source.
	// It is safe to call concurrently with SignTx
	Reload(ctx context.Context) error
	// Close zeroes the private key material the Provider holds in memory. The
	// raw, local-keystore and kms-mock backends keep an *ecdsa.PrivateKey for
	// the process lifetime; vault holds none, so its Close does nothing.
	// Call it once at shutdown: the key-holding backends cannot sign afterwards.
	// Zeroing is best effort, because the Go runtime may already have copied
	// the bytes elsewhere through garbage collection or stack growth.
	Close() error
}

// zeroECDSAKey overwrites priv's private scalar with zeros for every backend
// that holds a key (raw, local-keystore, kms-mock).
//
// SetInt64(0) alone is not enough: it only truncates the big.Int's backing
// slice and leaves the secret words in the heap, where a core dump or swap
// read can recover them. Overwrite the words through the mutable Bits() slice
// first, then normalize the value to zero.
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

// zeroBytes overwrites b in place, for backends that keep raw key bytes
// alongside the parsed *ecdsa.PrivateKey
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

	// "vault" mode uses a Transit-compatible backend. The key name for a role
	// is VaultKeyPrefix+role, such as "rwa-compliance". VaultToken needs read
	// access to transit/keys/<name> and update access to transit/sign/<name>;
	// scope its policy to those paths.
	VaultAddr      string
	VaultToken     string
	VaultKeyPrefix string

	// KMSMockSeedHex seeds "kms-mock" per-role keys: the key is the keccak256
	// of the seed and the role name. An empty seed generates a random key per
	// process and logs its address, which changes on every restart
	KMSMockSeedHex string
}

// Load resolves role to a Provider for cfg.Mode, which defaults to "raw".
// It returns (nil, nil) when "raw" mode has no key (RawHexKey is empty), so an
// absent key leaves the role's actions unavailable instead of failing startup.
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
		// Raw is also what an unset KEY_PROVIDER_MODE resolves to, so the
		// warning is the only signal that the least secure backend is in use
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
