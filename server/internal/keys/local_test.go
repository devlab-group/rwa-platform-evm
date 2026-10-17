package keys

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// writeTestKeystore creates a fresh encrypted dir/role.json and dir/<passwordFileName>,
// returning dir
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
	// the keystore JSON carries the address of the key it encrypts
	var ks struct {
		Address string `json:"address"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "compliance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &ks); err != nil {
		t.Fatal(err)
	}
	if want := common.HexToAddress(ks.Address); addr != want {
		t.Errorf("Address = %s, want the keystore's %s", addr.Hex(), want.Hex())
	}
	chainID := big.NewInt(31337)
	to := common.HexToAddress("0x00000000000000000000000000000000000042")
	signed, err := p.SignTx(context.Background(), types.NewTx(&types.LegacyTx{Gas: 21000, GasPrice: big.NewInt(1), To: &to}), chainID)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	if sender, err := types.Sender(types.LatestSignerForChainID(chainID), signed); err != nil || sender != addr {
		t.Errorf("signed transaction recovers to %s (%v), want %s", sender.Hex(), err, addr.Hex())
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
	if _, err := Load("compliance", Config{
		Mode: ModeLocalKeystore, KeystoreDir: dir, PasswordFile: filepath.Join(dir, "password.txt"),
	}); err == nil {
		t.Fatal("expected an error for a role with no compliance.json in KeystoreDir")
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

	// Replace the keystore file and password to simulate rotating the key on disk
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

// TestLocalKeystoreCloseZeroesKey checks that Close zeroes the decrypted key
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

func TestLocalKeystoreSignTxIsBoundToTheChainID(t *testing.T) {
	dir := writeTestKeystore(t, "compliance", "s3cret", "password.txt")
	p, err := Load("compliance", Config{
		Mode: ModeLocalKeystore, KeystoreDir: dir, PasswordFile: filepath.Join(dir, "password.txt"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr, _ := p.Address(context.Background())
	to := common.HexToAddress("0x00000000000000000000000000000000000042")
	chainID, otherChain := big.NewInt(31337), big.NewInt(1)

	txs := map[string]*types.Transaction{
		"dynamic fee": types.NewTx(&types.DynamicFeeTx{ChainID: chainID, Gas: 21000, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), To: &to, Value: big.NewInt(0)}),
		"legacy":      types.NewTx(&types.LegacyTx{Gas: 21000, GasPrice: big.NewInt(2), To: &to, Value: big.NewInt(0)}),
	}
	for name, tx := range txs {
		t.Run(name, func(t *testing.T) {
			signed, err := p.SignTx(context.Background(), tx, chainID)
			if err != nil {
				t.Fatalf("SignTx: %v", err)
			}
			if signed.ChainId().Cmp(chainID) != 0 {
				t.Errorf("signed chain id = %s, want %s", signed.ChainId(), chainID)
			}
			got, err := types.Sender(types.LatestSignerForChainID(chainID), signed)
			if err != nil || got != addr {
				t.Errorf("sender on the signing chain = %s (%v), want %s", got.Hex(), err, addr.Hex())
			}
			// Replay on another chain must not recover to the key's address
			if other, err := types.Sender(types.LatestSignerForChainID(otherChain), signed); err == nil && other == addr {
				t.Errorf("the signature also recovers to %s on chain %s: it is replayable", addr.Hex(), otherChain)
			}
		})
	}
}
