package keystore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

const testNativePassword = "a-reasonably-strong-password-99"

func TestCreateNative_RoundTrip(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	wantAddr := crypto.PubkeyToAddress(priv.PublicKey)

	data, err := CreateNative(priv, testNativePassword)
	if err != nil {
		t.Fatalf("CreateNative: %v", err)
	}

	path := filepath.Join(t.TempDir(), "auditor.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	gotPriv, gotAddr, err := Load(path, testNativePassword)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer Zero(gotPriv)
	if gotAddr != wantAddr {
		t.Errorf("address = %s, want %s", gotAddr.Hex(), wantAddr.Hex())
	}
	if gotPriv.D.Cmp(priv.D) != 0 {
		t.Error("recovered private key does not match original")
	}
}

func TestCreateNative_RejectsWeakPassword(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	if _, err := CreateNative(priv, "short"); err == nil {
		t.Fatal("expected CreateNative to reject a password below the minimum-password policy")
	}
}

func TestLoadNative_WrongPasswordFails(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	data, err := CreateNative(priv, testNativePassword)
	if err != nil {
		t.Fatalf("CreateNative: %v", err)
	}
	path := filepath.Join(t.TempDir(), "auditor.json")
	os.WriteFile(path, data, 0o600)

	if _, _, err := Load(path, "definitely-the-wrong-password-1"); err == nil {
		t.Fatal("expected an error for a wrong password")
	}
}

// TestLoadNative_RejectsDowngradedKDFParams checks that a native-format
// keystore file whose kdfparams have been edited down
// below the policy floor -- e.g. by an attacker who does not have the
// password but can modify the file to make brute-forcing cheaper -- must
// be refused before any decryption is attempted.
func TestLoadNative_RejectsDowngradedKDFParams(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	data, err := CreateNative(priv, testNativePassword)
	if err != nil {
		t.Fatalf("CreateNative: %v", err)
	}

	var f nativeKeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	f.Crypto.KDFParams.Time = 1 // below MinArgon2Time
	tampered, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "auditor.json")
	os.WriteFile(path, tampered, 0o600)

	if _, _, err := Load(path, testNativePassword); err == nil {
		t.Fatal("expected Load to reject downgraded Argon2id parameters")
	} else if !strings.Contains(err.Error(), "weaker than the minimum policy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIsNativeFormat(t *testing.T) {
	if !isNativeFormat([]byte(`{"format":"rwa-argon2id-v1"}`)) {
		t.Error("expected native-format detection to succeed")
	}
	if isNativeFormat([]byte(`{"version":3,"crypto":{}}`)) {
		t.Error("expected a standard V3 keystore JSON not to be detected as native")
	}
}

// --- maximum KDF parameters and exact-length validation ---

// tamperedNativeFile round-trips a fresh native keystore through JSON,
// letting the test mutate specific fields before Load sees the result.
func tamperedNativeFile(t *testing.T, mutate func(*nativeKeyFile)) string {
	t.Helper()
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	data, err := CreateNative(priv, testNativePassword)
	if err != nil {
		t.Fatalf("CreateNative: %v", err)
	}
	var f nativeKeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	mutate(&f)
	tampered, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "auditor.json")
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadNative_RejectsExtremeKDFParams(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*nativeKeyFile)
	}{
		{"time", func(f *nativeKeyFile) { f.Crypto.KDFParams.Time = MaxArgon2Time + 1 }},
		{"memory", func(f *nativeKeyFile) { f.Crypto.KDFParams.Memory = MaxArgon2MemoryKiB + 1 }},
		{"threads", func(f *nativeKeyFile) { f.Crypto.KDFParams.Threads = MaxArgon2Threads + 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tamperedNativeFile(t, tc.mutate)
			if _, _, err := Load(path, testNativePassword); err == nil {
				t.Fatal("expected Load to reject Argon2id parameters above the maximum policy")
			} else if !strings.Contains(err.Error(), "exceed the maximum policy") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadNative_RejectsWrongNonceLengthWithoutPanicking(t *testing.T) {
	for _, nonceLen := range []int{0, 1, 5, 11, 13, 32, 1000} {
		t.Run(fmt.Sprint(nonceLen), func(t *testing.T) {
			path := tamperedNativeFile(t, func(f *nativeKeyFile) {
				f.Crypto.Nonce = hexOfLen(t, nonceLen)
			})
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Load panicked on a %d-byte nonce: %v", nonceLen, r)
					}
				}()
				if _, _, err := Load(path, testNativePassword); err == nil {
					t.Fatal("expected Load to reject a wrong-length nonce")
				} else if !strings.Contains(err.Error(), "want exactly") {
					t.Errorf("unexpected error: %v", err)
				}
			}()
		})
	}
}

func TestLoadNative_RejectsWrongSaltLength(t *testing.T) {
	path := tamperedNativeFile(t, func(f *nativeKeyFile) {
		f.Crypto.KDFParams.Salt = hexOfLen(t, 4)
	})
	if _, _, err := Load(path, testNativePassword); err == nil {
		t.Fatal("expected Load to reject a wrong-length salt")
	} else if !strings.Contains(err.Error(), "want exactly") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadNative_RejectsWrongCiphertextLength(t *testing.T) {
	for _, ctLen := range []int{0, 1, 16, 47, 49, 1000} {
		t.Run(fmt.Sprint(ctLen), func(t *testing.T) {
			path := tamperedNativeFile(t, func(f *nativeKeyFile) {
				f.Crypto.CipherText = hexOfLen(t, ctLen)
			})
			if _, _, err := Load(path, testNativePassword); err == nil {
				t.Fatal("expected Load to reject a wrong-length ciphertext")
			} else if !strings.Contains(err.Error(), "want exactly") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadNative_RejectsMalformedHexWithoutPanicking(t *testing.T) {
	for _, field := range []string{"salt", "nonce", "ciphertext"} {
		t.Run(field, func(t *testing.T) {
			path := tamperedNativeFile(t, func(f *nativeKeyFile) {
				switch field {
				case "salt":
					f.Crypto.KDFParams.Salt = "not-hex!!"
				case "nonce":
					f.Crypto.Nonce = "not-hex!!"
				case "ciphertext":
					f.Crypto.CipherText = "not-hex!!"
				}
			})
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Load panicked on malformed hex in %s: %v", field, r)
				}
			}()
			if _, _, err := Load(path, testNativePassword); err == nil {
				t.Fatalf("expected Load to reject malformed hex in %s", field)
			}
		})
	}
}

// hexOfLen returns the hex encoding of n arbitrary bytes.
func hexOfLen(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return fmt.Sprintf("%x", b)
}
