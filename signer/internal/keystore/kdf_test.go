package keystore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gokeystore "github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestCheckV3KDFStrength_RejectsWeakScrypt(t *testing.T) {
	const weak = `{"crypto":{"kdf":"scrypt","kdfparams":{"n":4096,"r":8,"p":6}}}`
	if err := checkV3KDFStrength([]byte(weak)); err == nil {
		t.Fatal("expected rejection of scrypt N=4096 (below policy floor)")
	} else if !strings.Contains(err.Error(), "weaker than the minimum policy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckV3KDFStrength_RejectsWeakPBKDF2(t *testing.T) {
	const weak = `{"crypto":{"kdf":"pbkdf2","kdfparams":{"c":1000}}}`
	if err := checkV3KDFStrength([]byte(weak)); err == nil {
		t.Fatal("expected rejection of pbkdf2 c=1000 (below policy floor)")
	}
}

func TestCheckV3KDFStrength_RejectsUnknownKDF(t *testing.T) {
	const bogus = `{"crypto":{"kdf":"md5crypt","kdfparams":{}}}`
	if err := checkV3KDFStrength([]byte(bogus)); err == nil {
		t.Fatal("expected rejection of an unrecognized kdf")
	}
}

func TestCheckV3KDFStrength_AcceptsPolicyStrengthScrypt(t *testing.T) {
	strong := `{"crypto":{"kdf":"scrypt","kdfparams":{"n":` +
		"131072" + `,"r":8,"p":1}}}`
	if err := checkV3KDFStrength([]byte(strong)); err != nil {
		t.Errorf("expected policy-strength scrypt params to be accepted: %v", err)
	}
}

func TestCheckV3KDFStrength_AcceptsPolicyStrengthPBKDF2(t *testing.T) {
	const strong = `{"crypto":{"kdf":"pbkdf2","kdfparams":{"c":600000}}}`
	if err := checkV3KDFStrength([]byte(strong)); err != nil {
		t.Errorf("expected policy-strength pbkdf2 params to be accepted: %v", err)
	}
}

// TestLoad_RejectsLightScryptKeystore is the end-to-end version of
// TestCheckV3KDFStrength_RejectsWeakScrypt: a real keystore file encrypted
// with go-ethereum's own "light" parameters (used by wallets that
// prioritize unlock speed over brute-force resistance) must be refused by
// Load before the password is even tried.
func TestLoad_RejectsLightScryptKeystore(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dir := t.TempDir()
	ks := gokeystore.NewKeyStore(dir, gokeystore.LightScryptN, gokeystore.LightScryptP)
	account, err := ks.ImportECDSA(key, "correct-password-1234")
	if err != nil {
		t.Fatalf("ImportECDSA: %v", err)
	}

	if _, _, err := Load(account.URL.Path, "correct-password-1234"); err == nil {
		t.Fatal("expected Load to reject a light-scrypt keystore regardless of a correct password")
	} else if !strings.Contains(err.Error(), "weaker than the minimum policy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoad_RejectsGroupReadableKeystoreFile(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dir := t.TempDir()
	ks := gokeystore.NewKeyStore(dir, MinScryptN, MinScryptP)
	account, err := ks.ImportECDSA(key, "correct-password-1234")
	if err != nil {
		t.Fatalf("ImportECDSA: %v", err)
	}
	if err := os.Chmod(account.URL.Path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, _, err := Load(account.URL.Path, "correct-password-1234"); err == nil {
		t.Fatal("expected Load to reject a group-readable keystore file")
	} else if !strings.Contains(err.Error(), "readable") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoad_RejectsMissingFile(t *testing.T) {
	if _, _, err := Load(filepath.Join(t.TempDir(), "missing.json"), "whatever"); err == nil {
		t.Fatal("expected an error for a missing keystore file")
	}
}

// --- maximum KDF parameters, shape validation, size caps ---

func TestCheckV3KDFStrength_RejectsExtremeScryptN(t *testing.T) {
	extreme := `{"crypto":{"kdf":"scrypt","kdfparams":{"n":1073741824,"r":8,"p":1}}}` // 2^30
	if err := checkV3KDFStrength([]byte(extreme)); err == nil {
		t.Fatal("expected rejection of an extreme scrypt N (memory/CPU exhaustion attempt)")
	} else if !strings.Contains(err.Error(), "exceed the maximum policy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckV3KDFStrength_RejectsExtremeScryptRProduct(t *testing.T) {
	// N and P individually within their own ceilings, but N*r pushes
	// scrypt's ~128*N*r memory usage past the absolute backstop.
	extreme := `{"crypto":{"kdf":"scrypt","kdfparams":{"n":4194304,"r":64,"p":1}}}` // N=2^22, r=64
	if err := checkV3KDFStrength([]byte(extreme)); err == nil {
		t.Fatal("expected rejection of a scrypt N*r product exceeding the memory ceiling")
	} else if !strings.Contains(err.Error(), "MiB") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckV3KDFStrength_RejectsNonPowerOfTwoN(t *testing.T) {
	bogus := `{"crypto":{"kdf":"scrypt","kdfparams":{"n":200000,"r":8,"p":1}}}` // not a power of 2
	if err := checkV3KDFStrength([]byte(bogus)); err == nil {
		t.Fatal("expected rejection of a non-power-of-two scrypt N")
	} else if !strings.Contains(err.Error(), "power of two") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckV3KDFStrength_RejectsExtremePBKDF2(t *testing.T) {
	extreme := `{"crypto":{"kdf":"pbkdf2","kdfparams":{"c":2000000000}}}`
	if err := checkV3KDFStrength([]byte(extreme)); err == nil {
		t.Fatal("expected rejection of an extreme pbkdf2 iteration count")
	} else if !strings.Contains(err.Error(), "exceeds the maximum policy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoad_RejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.json")
	huge := make([]byte, MaxKeystoreFileBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, _, err := Load(path, "whatever"); err == nil {
		t.Fatal("expected Load to reject an oversized keystore file before parsing it")
	} else if !strings.Contains(err.Error(), "maximum") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoad_RejectsMalformedJSONWithoutPanicking(t *testing.T) {
	for _, bad := range []string{
		``,
		`not json at all`,
		`{"crypto":`,
		`{"crypto":{"kdf":"scrypt","kdfparams":"not an object"}}`,
		`{"crypto":{"kdf":"scrypt","kdfparams":{"n":"not a number","r":8,"p":1}}}`,
		`null`,
		`[]`,
		`42`,
	} {
		path := filepath.Join(t.TempDir(), "keystore.json")
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Load panicked on malformed input %q: %v", bad, r)
				}
			}()
			if _, _, err := Load(path, "whatever"); err == nil {
				t.Errorf("expected an error for malformed keystore JSON %q", bad)
			}
		}()
	}
}

// TestLoad_RejectsV3WithPanickyDecryptKeyParams is a belt-and-suspenders
// check that even if checkV3KDFStrength somehow let a bad set of
// parameters through, Load's recover in decryptV3Recovered turns any
// resulting panic from go-ethereum's DecryptKey into an ordinary error.
// It directly exercises decryptV3Recovered against a keystore JSON whose
// kdfparams are policy-strength-valid but otherwise structurally odd
// (missing ciphertext), which go-ethereum's own scrypt/passphrase code
// must handle -- this test's job is only to confirm that whatever happens
// inside DecryptKey, Load never panics.
func TestDecryptV3Recovered_NeverPanics(t *testing.T) {
	bogus := []byte(`{"crypto":{"kdf":"scrypt","kdfparams":{"n":131072,"r":8,"p":1,"salt":"00","dklen":32},"ciphertext":"","mac":"","cipher":"aes-128-ctr","cipherparams":{"iv":"00"}},"version":3}`)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("decryptV3Recovered panicked: %v", r)
		}
	}()
	if _, _, err := decryptV3Recovered(bogus, "whatever"); err == nil {
		t.Error("expected an error for a structurally bogus V3 keystore")
	}
}
