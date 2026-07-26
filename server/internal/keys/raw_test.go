package keys

import (
	"bytes"
	"context"
	"log"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const testHexKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80" // anvil acct0

// captureLog redirects the standard logger for the duration of fn and
// returns everything it wrote.
func captureLog(fn func()) string {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

// TestLoadRawModeLogsWarning is the K1 regression test: raw mode is the
// silent default (defaultModeIfEmpty), so it must warn just as loudly as
// kms-mock does, or an operator who forgot to set KEY_PROVIDER_MODE would
// get no signal they're running the least-secure backend.
func TestLoadRawModeLogsWarning(t *testing.T) {
	out := captureLog(func() {
		if _, err := Load("compliance", Config{RawHexKey: testHexKey}); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "raw") {
		t.Errorf("Load in raw mode did not log a WARNING; got: %q", out)
	}
}

func TestLoadRawModeLogsWarningWhenExplicit(t *testing.T) {
	out := captureLog(func() {
		if _, err := Load("relayer", Config{Mode: ModeRaw, RawHexKey: testHexKey}); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING") {
		t.Errorf("Load with explicit Mode: ModeRaw did not log a WARNING; got: %q", out)
	}
}

func TestLoadRawModeEmptyKeyReturnsNilNoError(t *testing.T) {
	p, err := Load("compliance", Config{Mode: ModeRaw})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p != nil {
		t.Errorf("Provider = %v, want nil (empty RawHexKey means this role has no key configured)", p)
	}
}

func TestLoadRawModeIsDefault(t *testing.T) {
	p, err := Load("compliance", Config{RawHexKey: testHexKey}) // no Mode set
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p == nil {
		t.Fatal("Provider = nil, want a raw-mode Provider")
	}
	addr, err := p.Address(context.Background())
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	if addr != common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266") {
		t.Errorf("Address = %s, want anvil acct0", addr.Hex())
	}
}

func TestLoadRawModeInvalidHexErrors(t *testing.T) {
	if _, err := Load("compliance", Config{Mode: ModeRaw, RawHexKey: "not-hex"}); err == nil {
		t.Fatal("expected an error for an invalid hex key")
	}
}

func TestRawKeyProviderSignsAndRecovers(t *testing.T) {
	p, err := Load("relayer", Config{Mode: ModeRaw, RawHexKey: testHexKey})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr, err := p.Address(context.Background())
	if err != nil {
		t.Fatalf("Address: %v", err)
	}

	chainID := big.NewInt(31337)
	to := common.HexToAddress("0x00000000000000000000000000000000000042")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: chainID, Nonce: 0, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2),
		Gas: 21000, To: &to, Value: big.NewInt(0),
	})
	signed, err := p.SignTx(context.Background(), tx, chainID)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	signer := types.LatestSignerForChainID(chainID)
	recovered, err := types.Sender(signer, signed)
	if err != nil {
		t.Fatalf("recovering sender: %v", err)
	}
	if recovered != addr {
		t.Errorf("recovered sender = %s, want %s", recovered.Hex(), addr.Hex())
	}
}

func TestRawKeyProviderReload(t *testing.T) {
	p, err := newRawKeyProvider(testHexKey)
	if err != nil {
		t.Fatalf("newRawKeyProvider: %v", err)
	}
	if err := p.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	addr, _ := p.Address(context.Background())
	if addr != crypto.PubkeyToAddress(p.key.PublicKey) {
		t.Errorf("Reload changed the address unexpectedly")
	}
}

// TestRawKeyProviderCloseZeroesKey is the K2 regression test: Close must
// overwrite the private scalar so it's not sitting in memory indefinitely
// after the process is done signing with it.
func TestRawKeyProviderCloseZeroesKey(t *testing.T) {
	p, err := newRawKeyProvider(testHexKey)
	if err != nil {
		t.Fatalf("newRawKeyProvider: %v", err)
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
	// Sign() == 0 alone would pass even if the words were merely resliced
	// away, so check the backing words directly — that memory is what a core
	// dump or swap read would recover.
	for _, w := range p.key.D.Bits() {
		if w != 0 {
			t.Error("Close resliced the scalar but left its backing words intact")
			break
		}
	}
	// Reload's key source must be scrubbed too; a retained copy of the raw
	// scalar is just as recoverable as the parsed one.
	for _, b := range p.raw {
		if b != 0 {
			t.Error("Close did not zero the retained raw key bytes")
			break
		}
	}
}
