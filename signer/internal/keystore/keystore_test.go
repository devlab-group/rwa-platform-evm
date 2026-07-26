package keystore

import (
	"crypto/ecdsa"
	"testing"

	gokeystore "github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type keyAndAddr struct {
	priv *ecdsa.PrivateKey
	addr common.Address
}

// newTestKeystoreFile creates a real V3 keystore file on disk (via
// go-ethereum's own KeyStore.ImportECDSA, the same code path a real
// operator would use) and returns its path and the imported key/address.
func newTestKeystoreFile(t *testing.T, passphrase string) (path string, want *keyAndAddr) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dir := t.TempDir()
	// MinScryptN/P (not LightScryptN/P): the signer now enforces a KDF
	// strength floor on every V3 load (kdf.go), so a keystore encrypted
	// with go-ethereum's "light" parameters would be rejected before Load
	// even tries the password.
	ks := gokeystore.NewKeyStore(dir, MinScryptN, MinScryptP)
	account, err := ks.ImportECDSA(key, passphrase)
	if err != nil {
		t.Fatalf("ImportECDSA: %v", err)
	}
	return account.URL.Path, &keyAndAddr{priv: key, addr: account.Address}
}

func TestLoad_RoundTrip(t *testing.T) {
	path, want := newTestKeystoreFile(t, "correct horse battery staple")

	gotPriv, gotAddr, err := Load(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer Zero(gotPriv)

	if gotAddr != want.addr {
		t.Errorf("address = %s, want %s", gotAddr.Hex(), want.addr.Hex())
	}
	if gotPriv.D.Cmp(want.priv.D) != 0 {
		t.Error("recovered private key does not match original")
	}
}

func TestLoad_WrongPassword(t *testing.T) {
	path, _ := newTestKeystoreFile(t, "right-password")
	if _, _, err := Load(path, "wrong-password"); err == nil {
		t.Fatal("expected error for wrong password")
	}
}

func TestZero_ClearsScalar(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	Zero(priv)
	if priv.D.Sign() != 0 {
		t.Error("Zero did not clear the private scalar")
	}
}

func TestZero_NilSafe(t *testing.T) {
	Zero(nil) // must not panic
}

func TestZeroBytes_Clears(t *testing.T) {
	b := []byte("correct horse battery staple")
	ZeroBytes(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("ZeroBytes did not clear byte %d", i)
		}
	}
}

func TestZeroBytes_NilAndEmptySafe(t *testing.T) {
	ZeroBytes(nil)      // must not panic
	ZeroBytes([]byte{}) // must not panic
}

// TestZero_OverwritesBackingWords pins the reason Zero does more than
// SetInt64(0): the scalar's backing words must actually be overwritten.
// SetInt64(0) alone only reslices them away, leaving the whole key readable in
// the heap -- and a core dump or a swapped page taken after signing would
// still yield it.
func TestZero_OverwritesBackingWords(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// Bits() aliases the big.Int's storage, so this keeps a view of that
	// memory after D itself has been resliced to zero length.
	words := key.D.Bits()
	if len(words) == 0 {
		t.Fatal("generated key has no scalar words")
	}
	nonZero := false
	for _, w := range words {
		if w != 0 {
			nonZero = true
		}
	}
	if !nonZero {
		t.Fatal("generated key scalar is all zero words")
	}

	Zero(key)

	if key.D.Sign() != 0 {
		t.Errorf("D = %s after Zero, want 0", key.D.String())
	}
	for i, w := range words {
		if w != 0 {
			t.Errorf("backing word %d still holds key material (%#x) after Zero", i, w)
		}
	}
}

// TestZero_NoScalarIsSafe covers a key struct whose D was never set, which
// Zero must skip rather than dereference.
func TestZero_NoScalarIsSafe(t *testing.T) {
	Zero(&ecdsa.PrivateKey{})
}
