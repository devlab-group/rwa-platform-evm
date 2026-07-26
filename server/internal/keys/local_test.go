package keys

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// writeTestKeystore encrypts priv into dir/role.json with password, using
// the same go-ethereum keystore machinery `cast wallet import` and the
// signer CLI's --keystore flag use, and dir/<passwordFileName> holding
// password. Returns dir.
func writeTestKeystore(t *testing.T, role, password string, passwordFileName string) string {
	t.Helper()
	dir := t.TempDir()
	ks := keystore.NewKeyStore(t.TempDir(), keystore.LightScryptN, keystore.LightScryptP)
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	account, err := ks.ImportECDSA(priv, password)
	if err != nil {
		t.Fatalf("ImportECDSA: %v", err)
	}
	raw, err := os.ReadFile(account.URL.Path)
	if err != nil {
		t.Fatalf("reading generated keystore file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, role+".json"), raw, 0o600); err != nil {
		t.Fatalf("writing %s.json: %v", role, err)
	}
	if err := os.WriteFile(filepath.Join(dir, passwordFileName), []byte(password+"\n"), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	return dir
}

func TestLocalKeystoreLoadsAndSigns(t *testing.T) {
	dir := writeTestKeystore(t, "compliance", "s3cret", "password.txt")

	p, err := Load("compliance", Config{
		Mode: ModeLocalKeystore, KeystoreDir: dir, PasswordFile: filepath.Join(dir, "password.txt"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr, err := p.Address(context.Background())
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	if addr == (common.Address{}) {
		t.Error("Address is zero, want the decrypted key's real address")
	}
}

func TestLocalKeystoreWrongPasswordErrors(t *testing.T) {
	dir := writeTestKeystore(t, "compliance", "correct-password", "password.txt")
	if err := os.WriteFile(filepath.Join(dir, "wrong.txt"), []byte("wrong-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("compliance", Config{
		Mode: ModeLocalKeystore, KeystoreDir: dir, PasswordFile: filepath.Join(dir, "wrong.txt"),
	}); err == nil {
		t.Fatal("expected an error decrypting with the wrong password")
	}
}

func TestLocalKeystoreMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "password.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("relayer", Config{
		Mode: ModeLocalKeystore, KeystoreDir: dir, PasswordFile: filepath.Join(dir, "password.txt"),
	}); err == nil {
		t.Fatal("expected an error for a role with no relayer.json in KeystoreDir")
	}
}

func TestLocalKeystoreRequiresConfig(t *testing.T) {
	if _, err := Load("compliance", Config{Mode: ModeLocalKeystore}); err == nil {
		t.Fatal("expected an error with no KeystoreDir/PasswordFile configured")
	}
}

func TestLocalKeystoreReloadPicksUpReplacedFile(t *testing.T) {
	dir := writeTestKeystore(t, "compliance", "first-password", "password.txt")
	p, err := newLocalKeystoreProvider("compliance", dir, filepath.Join(dir, "password.txt"))
	if err != nil {
		t.Fatalf("newLocalKeystoreProvider: %v", err)
	}
	firstAddr, _ := p.Address(context.Background())

	// Replace the keystore file + password with a different key/password,
	// simulating an operator rotating the on-disk credential.
	dir2 := writeTestKeystore(t, "compliance", "second-password", "password.txt")
	newRaw, err := os.ReadFile(filepath.Join(dir2, "compliance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.path, newRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.passwordFile, []byte("second-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := p.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	secondAddr, _ := p.Address(context.Background())
	if secondAddr == firstAddr {
		t.Error("Reload did not pick up the replaced keystore file (address unchanged)")
	}
}

// TestLocalKeystoreCloseZeroesKey is the K2 regression test.
func TestLocalKeystoreCloseZeroesKey(t *testing.T) {
	dir := writeTestKeystore(t, "compliance", "s3cret", "password.txt")
	p, err := newLocalKeystoreProvider("compliance", dir, filepath.Join(dir, "password.txt"))
	if err != nil {
		t.Fatalf("newLocalKeystoreProvider: %v", err)
	}
	if p.key.D.Sign() == 0 {
		t.Fatal("test setup bug: key is already zero before Close")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if p.key.D.Sign() != 0 {
		t.Error("Close did not zero the private scalar")
	}
}
