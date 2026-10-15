// Package keystore loads/creates encrypted signing keys and maintains audit/throttle
// files. AuditEvent callers must exclude passwords and private keys from metadata.
package keystore

import (
	"crypto/ecdsa"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
)

// MaxKeystoreFileBytes caps accepted keystore size. Load checks Stat and post-read
// length, but os.ReadFile allocations are not capped if the file grows.
const MaxKeystoreFileBytes = 1 << 20 // 1 MiB

// Load decrypts NativeFormat (Argon2id/AES-256-GCM) or V3 and returns key/address. It
// rejects group/other permissions, checks size before/after reading and bounds KDF work
// before derivation. Call Zero after signing.
func Load(path, password string) (*ecdsa.PrivateKey, common.Address, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: stat %s: %w", path, err)
	}
	if err := checkKeystoreFilePermissions(path, info); err != nil {
		return nil, common.Address{}, err
	}
	if info.Size() > MaxKeystoreFileBytes {
		return nil, common.Address{}, fmt.Errorf("keystore: %s is %d bytes, larger than the %d-byte maximum for a keystore file; refusing to read it", path, info.Size(), int64(MaxKeystoreFileBytes))
	}
	keyJSON, err := os.ReadFile(path)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: reading %s: %w", path, err)
	}
	// Detect growth after Stat; os.ReadFile itself has no size cap
	if len(keyJSON) > MaxKeystoreFileBytes {
		return nil, common.Address{}, fmt.Errorf("keystore: %s grew to %d bytes while being read, larger than the %d-byte maximum; refusing to use it", path, len(keyJSON), int64(MaxKeystoreFileBytes))
	}

	if isNativeFormat(keyJSON) {
		return loadNative(keyJSON, password)
	}

	if err := checkV3KDFStrength(keyJSON); err != nil {
		return nil, common.Address{}, err
	}
	// Recover DecryptKey panics from malformed V3 input even after KDF checks
	priv, addr, err := decryptV3Recovered(keyJSON, password)
	if err != nil {
		return nil, common.Address{}, err
	}
	return priv, addr, nil
}

func decryptV3Recovered(keyJSON []byte, password string) (priv *ecdsa.PrivateKey, addr common.Address, err error) {
	defer func() {
		if r := recover(); r != nil {
			priv, addr, err = nil, common.Address{}, fmt.Errorf("keystore: decrypting key: internal error (recovered panic): %v", r)
		}
	}()
	key, decErr := keystore.DecryptKey(keyJSON, password)
	if decErr != nil {
		// go-ethereum's error text does not include the password
		return nil, common.Address{}, fmt.Errorf("keystore: decrypting key: %w", decErr)
	}
	priv = key.PrivateKey
	addr = key.Address
	// Release this reference; the caller zeroes priv
	key.PrivateKey = nil
	return priv, addr, nil
}

// checkKeystoreFilePermissions rejects group/other bits for both formats, like
// --password-file, reducing local access for offline brute force
func checkKeystoreFilePermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("keystore: %s is group- or world-readable (mode %s); refusing to read it; run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}

// Zero overwrites key.D's backing words before SetInt64(0), which alone only reslices
// storage. Runtime, stack-growth or library copies may remain.
func Zero(key *ecdsa.PrivateKey) {
	if key == nil || key.D == nil {
		return
	}
	words := key.D.Bits()
	for i := range words {
		words[i] = 0
	}
	key.D.SetInt64(0)
}

// ZeroBytes overwrites b in place; independent runtime copies may remain, as with Zero
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
