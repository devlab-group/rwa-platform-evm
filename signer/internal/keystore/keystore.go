// Package keystore loads a password-encrypted Ethereum keystore key for
// offline signing. Passwords and private key material are never logged.
package keystore

import (
	"crypto/ecdsa"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
)

// MaxKeystoreFileBytes bounds how large a file Load will read, capping total
// size before we allocate for it. A real keystore file -- either format --
// is a few KB at most; 1 MiB is
// generous headroom while still refusing to read an arbitrarily large file
// (e.g. an attacker-replaced or corrupted path, or the wrong file entirely)
// into memory before it has even been parsed.
const MaxKeystoreFileBytes = 1 << 20 // 1 MiB

// Load decrypts the keystore file at path using password and returns the
// private key and its address. The caller MUST call Zero on the returned
// key once signing is complete.
//
// path may be either this signer's own NativeFormat (Argon2id + AES-256-GCM,
// see native.go) or a standard Ethereum V3 keystore JSON; the format is
// detected from the file's own "format" field, so callers never need to
// specify which one they have. Either way, Load enforces this signer's
// minimum-*and-maximum*-KDF-strength policy, rejects a group/world-readable
// keystore file, and rejects an oversized file, before reading or parsing
// its contents.
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
	// A file can grow between Stat and ReadFile (TOCTOU); os.ReadFile itself
	// has no size cap, so re-check what was actually read rather than trust
	// the Stat result alone.
	if len(keyJSON) > MaxKeystoreFileBytes {
		return nil, common.Address{}, fmt.Errorf("keystore: %s grew to %d bytes while being read, larger than the %d-byte maximum; refusing to use it", path, len(keyJSON), int64(MaxKeystoreFileBytes))
	}

	if isNativeFormat(keyJSON) {
		return loadNative(keyJSON, password)
	}

	if err := checkV3KDFStrength(keyJSON); err != nil {
		return nil, common.Address{}, err
	}
	// DecryptKey belongs to go-ethereum, not this module, and its behavior on
	// a maliciously malformed (as opposed to merely weak-KDF) V3 file is not
	// something this signer controls or has fully vetted.
	// checkV3KDFStrength above already bounds the KDF parameters
	// DecryptKey will use, but this recover is a last-resort net: no
	// malformed keystore file should ever be able to crash the process
	// instead of producing an error.
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
		// go-ethereum's error text does not include the password, so this
		// is safe to surface, but never wrap/log the password itself.
		return nil, common.Address{}, fmt.Errorf("keystore: decrypting key: %w", decErr)
	}
	priv = key.PrivateKey
	addr = key.Address
	// Zero anything else DecryptKey may have allocated on the Key struct.
	key.PrivateKey = nil
	return priv, addr, nil
}

// checkKeystoreFilePermissions rejects a keystore file that is group- or
// world-readable, mirroring cmd/signer's checkPasswordFilePermissions for
// --password-file. A keystore
// file is encrypted at rest, so this is defense in depth rather than the
// sole protection: it still narrows the field of local accounts that can
// even attempt an offline brute-force of the file, and it is unconditional
// -- it applies to both keystore formats Load accepts.
func checkKeystoreFilePermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("keystore: %s is group- or world-readable (mode %s); refusing to read it -- run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}

// Zero overwrites the private scalar of key with zero bytes. It does not
// guarantee the Go runtime never copied the value elsewhere (garbage
// collector, stack growth), but it removes the primary in-memory copy as
// soon as signing is done.
//
// SetInt64(0) alone is not enough: it reslices big.Int's backing words to
// length zero without touching them, so the whole scalar stays readable in
// the heap until something happens to reuse that memory. For an air-gapped
// signer whose one job is holding the auditor's key, a core dump or a swapped
// page taken after signing would still hand over the key. Bits() returns the
// backing slice itself, so zeroing it clears the actual words first.
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

// ZeroBytes overwrites b in place with zero bytes. Same caveats as Zero:
// it removes the primary in-memory copy of whatever b held (e.g. a
// password read from a terminal or file) as soon as it is no longer
// needed, but cannot reach any copy the Go runtime made independently.
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
