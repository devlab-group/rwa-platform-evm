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

// NativeFormat identifies this signer's own keystore file format. It uses
// Argon2id, preferred for a custom keystore format because it is memory-hard.
// Load auto-detects a native-format file by its "format" field, so the same
// --keystore flag accepts either this or a standard Ethereum V3 JSON file.
const NativeFormat = "rwa-argon2id-v1"

// Minimum Argon2id parameters this signer accepts, both when creating a
// new native keystore and when loading one. The floor is enforced on
// *load*, not just at creation, for the same reason as the V3 scrypt/pbkdf2
// floor (kdf.go): a file with downgraded parameters must never be silently
// accepted just because the correct password is supplied. These match RFC
// 9106's "second recommended option" (time=3, memory=64 MiB, parallelism=4)
// for environments where the higher-memory first option is impractical.
const (
	MinArgon2Time      uint32 = 3
	MinArgon2MemoryKiB uint32 = 64 * 1024 // 64 MiB
	MinArgon2Threads   uint8  = 4
	argon2KeyLen       uint32 = 32
	argon2SaltLen             = 16
)

// Maximum Argon2id parameters this signer accepts. A corrupted or
// attacker-supplied keystore could otherwise request extreme memory/CPU
// before authentication. Checked before argon2.IDKey ever runs -- an
// attacker who can supply/corrupt a keystore file but not the password
// must not be able to make loading it exhaust memory or hang the process.
// 2 GiB / 100 passes / 64 threads is far above any parameters this signer
// itself would ever write (native.go's Create always uses exactly
// MinArgon2*), but generous enough not to reject a legitimate keystore
// hardened well beyond this signer's own floor.
const (
	MaxArgon2Time      uint32 = 100
	MaxArgon2MemoryKiB uint32 = 2 * 1024 * 1024 // 2 GiB
	MaxArgon2Threads   uint8  = 64
)

// Exact byte lengths this signer's native format requires. Argon2SaltLen
// is fixed by argon2SaltLen (Create always writes exactly this many salt
// bytes); the nonce and ciphertext lengths are fixed by AES-256-GCM's
// standard nonce size and this format's fixed 32-byte plaintext (the
// private key scalar) plus GCM's tag overhead -- both crypto/cipher
// constants, not configurable by this format, so a file declaring any
// other length is malformed by construction, not just weakly parameterized.
//
// Checking these BEFORE calling gcm.Open matters beyond input validation:
// Go's crypto/cipher GCM implementation panics -- does not return an error
// -- when given a nonce of the wrong length (verified directly against this
// toolchain's crypto/cipher). A keystore file is
// attacker-influenced input up until the password has been verified, so
// this signer must never reach that call with an unchecked length.
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

// isNativeFormat reports whether keyJSON declares itself as NativeFormat,
// so Load can dispatch between this format and a standard V3 keystore
// without requiring a separate --keystore-format flag.
func isNativeFormat(keyJSON []byte) bool {
	var hdr struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(keyJSON, &hdr); err != nil {
		return false
	}
	return hdr.Format == NativeFormat
}

// CreateNative encrypts priv with password using this signer's own
// Argon2id + AES-256-GCM keystore format and returns the file contents to
// write to disk. password MUST already satisfy ValidatePassword;
// CreateNative re-checks it defensively so it can never be bypassed by a
// caller that forgets to check first.
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

// loadNative decrypts a native-format keystore file. It enforces the same
// minimum-KDF-parameters policy on load as CreateNative applies on
// creation: a file whose kdfparams have been downgraded below MinArgon2* —
// whether by an older tool version or by tampering — is refused before any
// decryption is attempted. It also enforces a maximum and validates every
// decoded byte length exactly before any KDF or AEAD call is made.
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
		return nil, common.Address{}, fmt.Errorf("keystore: argon2id parameters (time=%d memory=%dKiB threads=%d) are weaker than the minimum policy (time>=%d memory>=%dKiB threads>=%d); refusing to unlock a keystore with downgraded KDF parameters", p.Time, p.Memory, p.Threads, MinArgon2Time, MinArgon2MemoryKiB, MinArgon2Threads)
	}
	// The maximum side of the same policy -- these are checked before
	// argon2.IDKey (the actually expensive call) ever runs.
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
	// crypto/cipher's GCM.Open panics -- it does not return an error -- if
	// the nonce it is given is not exactly the AEAD's configured nonce size.
	// This check is what turns a malformed keystore file into an ordinary
	// error instead of crashing the process.
	if len(nonce) != gcmStandardNonceSize {
		return nil, common.Address{}, fmt.Errorf("keystore: nonce is %d bytes, want exactly %d; refusing to unlock a malformed keystore", len(nonce), gcmStandardNonceSize)
	}
	ciphertext, err := hex.DecodeString(f.Crypto.CipherText)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("keystore: decoding ciphertext: %w", err)
	}
	// This format only ever encrypts a fixed 32-byte plaintext (the private
	// key scalar) with no additional data, so the ciphertext length is
	// exactly determined -- not merely bounded -- by the cipher's tag
	// overhead. Any other length is a corrupted or malicious file; reject
	// it before allocating a decryption buffer or running Argon2id at all.
	if len(ciphertext) != expectedCiphertextLen {
		return nil, common.Address{}, fmt.Errorf("keystore: ciphertext is %d bytes, want exactly %d; refusing to unlock a malformed keystore", len(ciphertext), expectedCiphertextLen)
	}

	return decryptNativeRecovered(password, salt, nonce, ciphertext, p, f.Address)
}

// decryptNativeRecovered performs the actual Argon2id + AES-256-GCM
// decryption. Every length loadNative can check statically has already
// been validated by the time this is called; the recover here is a
// last-resort net against any panic in the KDF/cipher stack this signer
// has not anticipated -- the goal is that every malformed input becomes an
// error, never a panic -- not a substitute for the checks above.
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
		// AES-GCM authentication failure: wrong password or corrupted/
		// tampered file. go-ethereum's DecryptKey error text for the same
		// situation ("could not decrypt key with given password") does not
		// distinguish the two either, for the same reason: neither should
		// leak which case it was to someone probing passwords.
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
