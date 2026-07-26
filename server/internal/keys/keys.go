// Package keys implements the server's hot-key abstraction: a KeyProvider
// interface (local-keystore|vault|kms-mock) plus rotation.
// Every hot-key role (compliance/pricer/relayer/deployer)
// resolves to a Provider — Address()+SignTx(), matching
// internal/blockchain.Signer structurally — through exactly one of three
// backends selected by KEY_PROVIDER_MODE:
//
//   - "raw": a plaintext hex private key from an env
//     var (COMPLIANCE_KEY, ...). Kept as the default so existing
//     deployments/tests/docs are not broken by this package's addition;
//     explicitly the least secure option — Load logs a loud warning
//     whenever this mode is active (default or explicit), the same way
//     kms-mock does, since an unset KEY_PROVIDER_MODE silently resolves
//     here otherwise.
//   - "local-keystore": a password-encrypted go-ethereum keystore JSON file
//     per role (same format signer/internal/keystore already uses), key
//     material decrypted into memory once at Load and held there — better
//     than plaintext-in-env, but still a hot key on this host.
//   - "vault": HashiCorp Vault's Transit secrets engine signs remotely over
//     HTTP; this server never sees the private key at all. Rotation is
//     Vault-native (`vault write -f transit/keys/<name>/rotate`) — signing
//     always asks for Transit's latest key version, so a rotation on the
//     Vault side takes effect on this server's very next signature with no
//     server-side action needed.
//   - "kms-mock": an explicit, clearly-non-production stand-in for a real
//     KMS (AWS KMS, GCP Cloud KMS, ...) — deterministic per-role keys
//     derived from a seed, so the Provider abstraction and its call sites
//     can be exercised in CI/local dev without a real KMS account. NEVER
//     use in production; Load logs a loud warning when this mode is active.
//
// "Rotation" beyond Vault's native support: Provider.Reload re-reads
// key material from its source (a changed keystore file + password on
// disk for local-keystore) without a process restart. main.go does not
// currently call Reload on a timer/signal — see Provider's doc comment —
// but every backend implements it so that wiring is a small addition, not
// a redesign, when an operator asks for it.
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

// Mode selects which backend Load uses.
type Mode string

const (
	ModeRaw            Mode = "raw"
	ModeLocalKeystore  Mode = "local-keystore"
	ModeVault          Mode = "vault"
	ModeKMSMock        Mode = "kms-mock"
	defaultModeIfEmpty      = ModeRaw
)

// Provider is a hot key that can report the address it signs for, sign a
// transaction, and reload its key material from its source (rotation
// support). It is structurally identical to internal/blockchain.Signer
// plus Reload/Close, so any Provider is usable anywhere a blockchain.Signer
// is expected without this package importing internal/blockchain.
type Provider interface {
	Address(ctx context.Context) (common.Address, error)
	SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
	// Reload re-reads/re-fetches key material from this Provider's source.
	// Safe to call at any time, including concurrently with SignTx (callers
	// needing atomicity around a specific tx should not call Reload
	// mid-flight, but Reload itself does not corrupt an in-progress sign).
	Reload(ctx context.Context) error
	// Close zeroes any private key material this Provider holds in memory
	// (the raw/local-keystore/kms-mock backends decrypt/derive a real
	// *ecdsa.PrivateKey and keep it for the process lifetime; vault never
	// holds one at all, so its Close is a no-op — see each backend's Zero
	// helper). Call once, at process shutdown; a Provider is unusable for
	// SignTx after Close returns. Mirrors signer/internal/keystore.Zero's
	// pattern for the same reason: best-effort, not a hard security
	// guarantee (the Go runtime may have copied the bytes elsewhere via GC
	// or stack growth before Close runs), but it removes the primary
	// in-memory copy as soon as it's no longer needed.
	Close() error
}

// zeroECDSAKey overwrites priv's private scalar with zero bytes, shared by
// every backend that holds a real key (raw, local-keystore, kms-mock).
//
// SetInt64(0) alone is not enough: it only reslices big.Int's backing nat to
// length 0, leaving the secret words intact in the heap where a core dump or
// swap read still recovers them. Overwrite the words through the mutable
// Bits() slice first, then normalize the value to zero.
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

// zeroBytes overwrites b in place, for the backends that retain raw key
// material alongside the parsed *ecdsa.PrivateKey.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Config bundles every backend's configuration. Only the fields the
// selected Mode actually needs are read; the rest may be zero-valued.
type Config struct {
	Mode Mode

	// "raw" mode: a hex private key (with or without 0x prefix).
	RawHexKey string

	// "local-keystore" mode: KeystoreDir/<role>.json decrypted with the
	// contents of PasswordFile (first line, trailing newline trimmed).
	KeystoreDir  string
	PasswordFile string

	// "vault" mode: HashiCorp Vault Transit secrets engine. Key name per
	// role is KeyNamePrefix+role (e.g. prefix "rwa-" + role "compliance" =
	// "rwa-compliance"). Token is a Vault token with sign/read capability
	// on transit/keys/<name> and transit/sign/<name> — read further
	// restricted in a real deployment via a Vault policy scoped to exactly
	// those paths (see docs/operator/operator-guide.md).
	VaultAddr      string
	VaultToken     string
	VaultKeyPrefix string

	// "kms-mock" mode: deterministic per-role keys derived from SeedHex (a
	// hex string of any length, hashed together with the role name); empty
	// SeedHex means "generate a random ephemeral key and log its address,
	// once, at Load" — fine for a one-off local smoke test, useless across
	// restarts since the address changes every time.
	KMSMockSeedHex string
}

// Load resolves role (e.g. "compliance", "pricer",
// "relayer", "deployer") to a Provider per cfg.Mode. An empty Mode defaults
// to "raw". Returns (nil, nil) — not an error — when "raw" mode has no key
// configured for this role (RawHexKey == ""): an absent hot key means "this
// role's actions are unavailable," not a startup failure.
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
		// Loud on purpose (mirrors kms-mock's warning below): ModeRaw is
		// also what an UNSET KEY_PROVIDER_MODE silently resolves to
		// (defaultModeIfEmpty), so without this, forgetting to configure
		// KEY_PROVIDER_MODE in production silently picks the least-secure
		// backend — a plaintext hex key held in memory for the process
		// lifetime — with no signal that anything unusual happened.
		log.Printf("keys: WARNING — role %q is using raw env-hex key provider. NOT for production; use local-keystore, vault, or kms-mock (set KEY_PROVIDER_MODE).", role)
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
