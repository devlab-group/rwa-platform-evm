package keystore

import (
	"encoding/json"
	"fmt"
)

// Minimum KDF parameters this signer accepts when loading a standard
// Ethereum V3 keystore. We insist on strong, explicitly configured scrypt
// or PBKDF2 parameters rather than trusting library defaults, and never
// silently downgrade them. These are checked on *every* load, not just at
// creation time, so a V3
// file that was generated elsewhere with weak parameters — or one an
// attacker who does not have the password has edited on disk to make
// brute-forcing cheaper — is refused rather than silently accepted.
//
// MinScryptN/R/P (2^17, r=8, p=1) match the OWASP Password Storage Cheat
// Sheet's scrypt guidance. MinPBKDF2Iterations (600,000, PBKDF2-HMAC-SHA256)
// matches OWASP's current PBKDF2 guidance. go-ethereum's own
// "StandardScryptN" (2^18) is stronger still, but this signer does not
// create new V3 files itself (see native.go: "signer keystore create"/
// "import" only ever write this signer's own Argon2id NativeFormat) --
// this floor is only the minimum an *externally*-generated V3 keystore
// must meet for Load to accept it, so it is pinned to a recognized
// strength bar (OWASP) rather than to go-ethereum's own constant.
const (
	MinScryptN          = 1 << 17
	MinScryptR          = 8
	MinScryptP          = 1
	MinPBKDF2Iterations = 600_000
)

// Maximum KDF parameters this signer accepts. A minimum work factor alone
// isn't enough: a corrupted or attacker-supplied keystore could request
// extreme memory/CPU before authentication, so we cap it too. These are
// checked BEFORE DecryptKey/scrypt/pbkdf2 ever run, on the same principle
// as the minimum:
// a keystore file's declared parameters are untrusted input up to the
// point the password has been verified, so both directions of "not what a
// legitimate keystore would ever declare" are rejected before any
// expensive work is done, not just before any *weak* work is done.
//
// MaxScryptN/R/P bound scrypt's memory usage (~128*N*r bytes) and CPU cost
// (~N*r*p) to sane multiples of go-ethereum's own StandardScryptN (2^18):
// MaxScryptN itself is 2^22 (32x the standard N), but maxScryptMemoryBytes
// is the real backstop, since N and r are independently attacker-chosen and
// either alone staying under its own ceiling does not bound their product.
// MaxPBKDF2Iterations bounds CPU time the same way scrypt's ceiling bounds
// CPU+memory.
const (
	MaxScryptN           = 1 << 22 // ~32x go-ethereum's StandardScryptN
	MaxScryptR           = 64
	MaxScryptP           = 16
	maxScryptMemoryBytes = 1 << 30 // 1 GiB absolute ceiling on scrypt's ~128*N*r memory usage
	MaxPBKDF2Iterations  = 10_000_000
)

// v3KDFHeader parses only the two fields of an Ethereum V3 keystore JSON
// needed to check KDF strength (crypto.kdf, crypto.kdfparams), without
// requiring the password. go-ethereum's own keystore.DecryptKey does not
// expose kdfparams before decrypting, and does not enforce any minimum —
// it accepts whatever parameters the file declares, however weak.
type v3KDFHeader struct {
	Crypto struct {
		KDF       string          `json:"kdf"`
		KDFParams json.RawMessage `json:"kdfparams"`
	} `json:"crypto"`
}

type scryptParams struct {
	N int `json:"n"`
	R int `json:"r"`
	P int `json:"p"`
}

type pbkdf2Params struct {
	C int `json:"c"`
}

// checkV3KDFStrength rejects a V3 keystore JSON whose declared KDF
// parameters fall outside this signer's policy window -- too weak (easy to
// brute-force) or too strong (a memory/CPU exhaustion attempt via a
// corrupted or attacker-supplied file). It is checked before
// DecryptKey is ever called, so a bad keystore is refused whether or not
// the caller supplies the correct password, and before any scrypt/pbkdf2
// work is attempted.
func checkV3KDFStrength(keyJSON []byte) error {
	var hdr v3KDFHeader
	if err := json.Unmarshal(keyJSON, &hdr); err != nil {
		return fmt.Errorf("keystore: parsing keystore JSON to check KDF parameters: %w", err)
	}
	switch hdr.Crypto.KDF {
	case "scrypt":
		var p scryptParams
		if err := json.Unmarshal(hdr.Crypto.KDFParams, &p); err != nil {
			return fmt.Errorf("keystore: parsing scrypt kdfparams: %w", err)
		}
		if p.N < MinScryptN || p.R < MinScryptR || p.P < MinScryptP {
			return fmt.Errorf("keystore: scrypt parameters (N=%d r=%d p=%d) are weaker than the minimum policy (N>=%d r>=%d p>=%d); refusing to unlock a keystore with downgraded KDF parameters -- re-encrypt it with stronger parameters (e.g. \"signer keystore create\") before use", p.N, p.R, p.P, MinScryptN, MinScryptR, MinScryptP)
		}
		// Validate scrypt's shape, not just its magnitude.
		// N must be a power of two greater than 1 -- golang.org/x/crypto/scrypt
		// requires this internally, but checking it ourselves gives a clear
		// error here rather than depending on however go-ethereum's own
		// scrypt call happens to fail (or not) on a malformed N.
		if p.N&(p.N-1) != 0 {
			return fmt.Errorf("keystore: scrypt N=%d is not a power of two; refusing to unlock a keystore with a malformed KDF parameter", p.N)
		}
		if p.N > MaxScryptN || p.R > MaxScryptR || p.P > MaxScryptP {
			return fmt.Errorf("keystore: scrypt parameters (N=%d r=%d p=%d) exceed the maximum policy (N<=%d r<=%d p<=%d); refusing to spend unbounded memory/CPU decrypting a keystore with these parameters before the password is even verified", p.N, p.R, p.P, MaxScryptN, MaxScryptR, MaxScryptP)
		}
		// N and r are independently attacker-chosen, so each staying under
		// its own ceiling does not bound their product: scrypt's memory use
		// is ~128*N*r bytes, checked here as an absolute backstop.
		if scryptMemory := uint64(p.N) * uint64(p.R) * 128; scryptMemory > maxScryptMemoryBytes {
			return fmt.Errorf("keystore: scrypt parameters (N=%d r=%d) would use ~%d MiB of memory, exceeding the %d MiB policy ceiling; refusing to unlock", p.N, p.R, scryptMemory/(1<<20), maxScryptMemoryBytes/(1<<20))
		}
	case "pbkdf2":
		var p pbkdf2Params
		if err := json.Unmarshal(hdr.Crypto.KDFParams, &p); err != nil {
			return fmt.Errorf("keystore: parsing pbkdf2 kdfparams: %w", err)
		}
		if p.C < MinPBKDF2Iterations {
			return fmt.Errorf("keystore: pbkdf2 iteration count (c=%d) is weaker than the minimum policy (c>=%d); refusing to unlock a keystore with downgraded KDF parameters -- re-encrypt it with stronger parameters (e.g. \"signer keystore create\") before use", p.C, MinPBKDF2Iterations)
		}
		if p.C > MaxPBKDF2Iterations {
			return fmt.Errorf("keystore: pbkdf2 iteration count (c=%d) exceeds the maximum policy (c<=%d); refusing to spend unbounded CPU decrypting a keystore with these parameters before the password is even verified", p.C, MaxPBKDF2Iterations)
		}
	default:
		return fmt.Errorf("keystore: unsupported or missing kdf %q in keystore JSON", hdr.Crypto.KDF)
	}
	return nil
}
