package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/argon2"
)

// NativeFormat identifies the Argon2id and AES-256-GCM keystore this signer writes.
// Load recognizes it by the "format" field; --keystore also accepts Ethereum V3 files.
const NativeFormat = "rwa-argon2id-v1"

// Minimum Argon2id work factors, enforced at creation and on every load.
// They match RFC 9106's second recommended option: 3 passes, 64 MiB and 4 threads.
const (
	MinArgon2Time      uint32 = 3
	MinArgon2MemoryKiB uint32 = 64 * 1024 // 64 MiB
	MinArgon2Threads   uint8  = 4
	argon2KeyLen       uint32 = 32
	argon2SaltLen             = 16
)

// Maximum Argon2id work factors limit resource use before the password is checked.
// Creation uses the MinArgon2 values; a loaded file may use stronger ones within these
// bounds.
const (
	MaxArgon2Time      uint32 = 100
	MaxArgon2MemoryKiB uint32 = 2 * 1024 * 1024 // 2 GiB
	MaxArgon2Threads   uint8  = 64
)

// Native files use a 16-byte salt, a 12-byte GCM nonce, and a 32-byte private key plus a
// 16-byte tag as ciphertext. Check the lengths before derivation and decryption, because
// GCM.Open panics on a wrong-sized nonce.
const (
	gcmStandardNonceSize  = 12
	gcmStandardTagSize    = 16
	expectedPlaintextLen  = 32 // crypto.FromECDSA's fixed output size
	expectedCiphertextLen = expectedPlaintextLen + gcmStandardTagSize
)

type nativeKeyFile struct {
	Format  string             `json:"format"`
	Address string             `json:"address"`
	Crypto  nativeCryptoParams `json:"crypto"`
}

type nativeCryptoParams struct {
	Cipher     string          `json:"cipher"` // "aes-256-gcm"
	CipherText string          `json:"ciphertext"`
	Nonce      string          `json:"nonce"`
	KDF        string          `json:"kdf"` // "argon2id"
	KDFParams  nativeKDFParams `json:"kdfparams"`
}

type nativeKDFParams struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // KiB
	Threads uint8  `json:"threads"`
	Salt    string `json:"salt"`
}

// isNativeFormat selects native loading from the "format" field
func isNativeFormat(keyJSON []byte) bool {
	var hdr struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(keyJSON, &hdr); err != nil {
		return false
	}
	return hdr.Format == NativeFormat
}

// CreateNative validates the password and encrypts priv with Argon2id and AES-256-GCM,
// returning the bytes to write to disk
func CreateNative(priv *ecdsa.PrivateKey, password string) ([]byte, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}

	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("keystore: generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, MinArgon2Time, MinArgon2MemoryKiB, MinArgon2Threads, argon2KeyLen)
	defer ZeroBytes(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("keystore: initializing cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keystore: initializing AEAD: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keystore: generating nonce: %w", err)
	}

	plain := crypto.FromECDSA(priv) // 32-byte big-endian scalar
	defer ZeroBytes(plain)
	ciphertext := gcm.Seal(nil, nonce, plain, nil)

	addr := crypto.PubkeyToAddress(priv.PublicKey)
	f := nativeKeyFile{
		Format:  NativeFormat,
		Address: addr.Hex(),
		Crypto: nativeCryptoParams{
			Cipher:     "aes-256-gcm",
			CipherText: hex.EncodeToString(ciphertext),
			Nonce:      hex.EncodeToString(nonce),
			KDF:        "argon2id",
			KDFParams: nativeKDFParams{
				Time:    MinArgon2Time,
				Memory:  MinArgon2MemoryKiB,
				Threads: MinArgon2Threads,
				Salt:    hex.EncodeToString(salt),
			},
		},
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("keystore: encoding native keystore: %w", err)
	}
	return append(data, '\n'), nil
}

// loadNative checks KDF bounds and decoded lengths before deriving and decrypting the key
func loadNative(keyJSON []byte, password string) (*ecdsa.PrivateKey, common.Address, error) {
	var f nativeKeyFile
	if err := json.Unmarshal(keyJSON, &f); err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: parsing native keystore JSON: %w", err)
	}
	if f.Crypto.Cipher != "aes-256-gcm" {
		return nil, common.Address{}, fmt.Errorf("keystore: unsupported cipher %q", f.Crypto.Cipher)
	}
	if f.Crypto.KDF != "argon2id" {
		return nil, common.Address{}, fmt.Errorf("keystore: unsupported kdf %q", f.Crypto.KDF)
	}
	p := f.Crypto.KDFParams
	if p.Time < MinArgon2Time || p.Memory < MinArgon2MemoryKiB || p.Threads < MinArgon2Threads {
		return nil, common.Address{}, fmt.Errorf("keystore: argon2id parameters (time=%d memory=%dKiB threads=%d) are weaker than the minimum policy (time>=%d memory>=%dKiB threads>=%d); refusing to unlock a keystore with downgraded KDF parameters; restore it from a backup or re-encrypt its key with \"signer keystore import\"", p.Time, p.Memory, p.Threads, MinArgon2Time, MinArgon2MemoryKiB, MinArgon2Threads)
	}
	// Cap resource use before argon2.IDKey
	if p.Time > MaxArgon2Time || p.Memory > MaxArgon2MemoryKiB || p.Threads > MaxArgon2Threads {
		return nil, common.Address{}, fmt.Errorf("keystore: argon2id parameters (time=%d memory=%dKiB threads=%d) exceed the maximum policy (time<=%d memory<=%dKiB threads<=%d); refusing to spend unbounded memory/CPU decrypting a keystore with these parameters before the password is even verified", p.Time, p.Memory, p.Threads, MaxArgon2Time, MaxArgon2MemoryKiB, MaxArgon2Threads)
	}

	salt, err := hex.DecodeString(p.Salt)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: decoding salt: %w", err)
	}
	if len(salt) != argon2SaltLen {
		return nil, common.Address{}, fmt.Errorf("keystore: salt is %d bytes, want exactly %d; refusing to unlock a malformed keystore", len(salt), argon2SaltLen)
	}
	nonce, err := hex.DecodeString(f.Crypto.Nonce)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: decoding nonce: %w", err)
	}
	// Reject an invalid nonce length before GCM.Open can panic
	if len(nonce) != gcmStandardNonceSize {
		return nil, common.Address{}, fmt.Errorf("keystore: nonce is %d bytes, want exactly %d; refusing to unlock a malformed keystore", len(nonce), gcmStandardNonceSize)
	}
	ciphertext, err := hex.DecodeString(f.Crypto.CipherText)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: decoding ciphertext: %w", err)
	}
	// The 32-byte key and fixed GCM tag determine the ciphertext length exactly
	if len(ciphertext) != expectedCiphertextLen {
		return nil, common.Address{}, fmt.Errorf("keystore: ciphertext is %d bytes, want exactly %d; refusing to unlock a malformed keystore", len(ciphertext), expectedCiphertextLen)
	}

	return decryptNativeRecovered(password, salt, nonce, ciphertext, p, f.Address)
}

// decryptNativeRecovered derives and decrypts after length checks, converting KDF/cipher
// panics to errors as a final safeguard
func decryptNativeRecovered(password string, salt, nonce, ciphertext []byte, p nativeKDFParams, declaredAddr string) (priv *ecdsa.PrivateKey, addr common.Address, err error) {
	defer func() {
		if r := recover(); r != nil {
			priv, addr, err = nil, common.Address{}, fmt.Errorf("keystore: internal error decrypting native keystore (recovered panic): %v", r)
		}
	}()

	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, argon2KeyLen)
	defer ZeroBytes(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: initializing cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: initializing AEAD: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Use one error for wrong passwords and corrupted ciphertext, as V3 decryption does
		return nil, common.Address{}, fmt.Errorf("keystore: could not decrypt key with given password")
	}
	defer ZeroBytes(plain)

	priv, err = crypto.ToECDSA(plain)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: recovered key material is invalid: %w", err)
	}
	addr = crypto.PubkeyToAddress(priv.PublicKey)
	declared := common.HexToAddress(declaredAddr)
	if addr != declared {
		return nil, common.Address{}, fmt.Errorf("keystore: recovered address %s does not match keystore file's declared address %s", addr.Hex(), declared.Hex())
	}
	return priv, addr, nil
}
