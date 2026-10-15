package keystore

import (
	"encoding/json"
	"fmt"
)

// V3 KDF floors are checked before derivation, matching OWASP's scrypt (N=2^17, r=8,
// p=1) and PBKDF2-HMAC-SHA256 (600,000 iterations) guidance; new files use NativeFormat
const (
	MinScryptN          = 1 << 17
	MinScryptR          = 8
	MinScryptP          = 1
	MinPBKDF2Iterations = 600_000
)

// V3 ceilings limit work before password authentication; scrypt also caps combined
// memory at 128*N*r
const (
	MaxScryptN           = 1 << 22 // 16x go-ethereum's StandardScryptN
	MaxScryptR           = 64
	MaxScryptP           = 16
	maxScryptMemoryBytes = 1 << 30 // 1 GiB cap on approximately 128*N*r bytes
	MaxPBKDF2Iterations  = 10_000_000
)

// v3KDFHeader reads parameters for strength checks before go-ethereum derives the key
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

// checkV3KDFStrength rejects malformed, weak or excessive KDF parameters before DecryptKey
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
			return fmt.Errorf("keystore: scrypt parameters (N=%d r=%d p=%d) are weaker than the minimum policy (N>=%d r>=%d p>=%d); refusing to unlock a keystore with downgraded KDF parameters; to keep this key, decrypt it with the tool that created it and re-encrypt it with \"signer keystore import\"", p.N, p.R, p.P, MinScryptN, MinScryptR, MinScryptP)
		}
		// Reject non-power-of-two scrypt N before derivation
		if p.N&(p.N-1) != 0 {
			return fmt.Errorf("keystore: scrypt N=%d is not a power of two; refusing to unlock a keystore with a malformed KDF parameter", p.N)
		}
		if p.N > MaxScryptN || p.R > MaxScryptR || p.P > MaxScryptP {
			return fmt.Errorf("keystore: scrypt parameters (N=%d r=%d p=%d) exceed the maximum policy (N<=%d r<=%d p<=%d); refusing to spend unbounded memory/CPU decrypting a keystore with these parameters before the password is even verified", p.N, p.R, p.P, MaxScryptN, MaxScryptR, MaxScryptP)
		}
		// Bound the combined 128*N*r allocation, not just each parameter
		if scryptMemory := uint64(p.N) * uint64(p.R) * 128; scryptMemory > maxScryptMemoryBytes {
			return fmt.Errorf("keystore: scrypt parameters (N=%d r=%d) would use ~%d MiB of memory, exceeding the %d MiB policy ceiling; refusing to unlock", p.N, p.R, scryptMemory/(1<<20), maxScryptMemoryBytes/(1<<20))
		}
	case "pbkdf2":
		var p pbkdf2Params
		if err := json.Unmarshal(hdr.Crypto.KDFParams, &p); err != nil {
			return fmt.Errorf("keystore: parsing pbkdf2 kdfparams: %w", err)
		}
		if p.C < MinPBKDF2Iterations {
			return fmt.Errorf("keystore: pbkdf2 iteration count (c=%d) is weaker than the minimum policy (c>=%d); refusing to unlock a keystore with downgraded KDF parameters; to keep this key, decrypt it with the tool that created it and re-encrypt it with \"signer keystore import\"", p.C, MinPBKDF2Iterations)
		}
		if p.C > MaxPBKDF2Iterations {
			return fmt.Errorf("keystore: pbkdf2 iteration count (c=%d) exceeds the maximum policy (c<=%d); refusing to spend unbounded CPU decrypting a keystore with these parameters before the password is even verified", p.C, MaxPBKDF2Iterations)
		}
	default:
		return fmt.Errorf("keystore: unsupported or missing kdf %q in keystore JSON", hdr.Crypto.KDF)
	}
	return nil
}
