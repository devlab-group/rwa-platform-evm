// Package keystore loads and creates the password-encrypted signing key, and keeps the
// unlock audit log and throttle. Passwords and private key material are never logged.
package keystore

import (
	"crypto/ecdsa"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
)

// MaxKeystoreFileBytes bounds the keystore file Load will read. A real keystore of
// either format is a few KB, so 1 MiB leaves ample room while refusing to read a
// replaced, corrupted or wrong file of arbitrary size into memory before parsing it
const MaxKeystoreFileBytes = 1 << 20 // 1 MiB

// Load decrypts a NativeFormat (Argon2id/AES-256-GCM) or Ethereum V3 keystore,
// selected by its format field, and returns the key and address. Reject group
// or other permissions and sizes above MaxKeystoreFileBytes before reading;
// check the format's KDF bounds before derivation. Call Zero after signing.
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
	// A file can grow between Stat and ReadFile and os.ReadFile has no size cap, so
	// check the length actually read as well
	if len(keyJSON) > MaxKeystoreFileBytes {
		return nil, common.Address{}, fmt.Errorf("keystore: %s grew to %d bytes while being read, larger than the %d-byte maximum; refusing to use it", path, len(keyJSON), int64(MaxKeystoreFileBytes))
	}

	if isNativeFormat(keyJSON) {
		return loadNative(keyJSON, password)
	}

	if err := checkV3KDFStrength(keyJSON); err != nil {
		return nil, common.Address{}, err
	}
	// checkV3KDFStrength bounds the KDF parameters, but go-ethereum's DecryptKey is not
	// vetted against maliciously malformed V3 files, so recover from a panic and return
	// an error instead of crashing the process
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
	// Drop the Key struct's reference to the key; the caller zeroes priv
	key.PrivateKey = nil
	return priv, addr, nil
}

// checkKeystoreFilePermissions rejects a keystore file with any group or other
// permission bit set, as cmd/signer's checkPasswordFilePermissions does for
// --password-file. The file is encrypted, so this is defense in depth: it narrows the
// set of local accounts that can attempt an offline brute force. It applies to both
// formats Load accepts.
func checkKeystoreFilePermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("keystore: %s is group- or world-readable (mode %s); refusing to read it; run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}

// Zero clears key's private-scalar backing words before resetting the big.Int.
// SetInt64(0) alone changes the slice length without clearing its old storage.
// This removes the primary scalar copy but cannot clear copies retained by
// the Go runtime, stack growth or libraries.
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

// ZeroBytes overwrites b in place with zero bytes. As with Zero, it removes the primary
// in-memory copy of whatever b held, such as a password read from a terminal or file,
// but cannot reach copies the Go runtime made independently.
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
