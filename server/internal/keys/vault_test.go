package keys

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// fakeVaultTransit serves just enough of Vault Transit's HTTP API
// (GET .../keys/:name, POST .../sign/:name) for vaultProvider to work
// against, backed by a real in-memory secp256k1 key so signatures round-
// trip through actual EC math rather than canned bytes.
type fakeVaultTransit struct {
	key      *ecdsa.PrivateKey
	pubBytes []byte
	token    string
	name     string
}

func newFakeVaultTransit(t *testing.T, keyName, token string) (*httptest.Server, *fakeVaultTransit) {
	t.Helper()
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeVaultTransit{key: priv, pubBytes: crypto.FromECDSAPub(&priv.PublicKey), token: token, name: keyName}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/transit/keys/"+keyName, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != f.token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		resp := transitKeyResponse{}
		resp.Data.LatestVersion = 1
		resp.Data.Keys = map[string]struct {
			PublicKey string `json:"public_key"`
		}{"1": {PublicKey: base64.StdEncoding.EncodeToString(f.pubBytes)}}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/v1/transit/sign/"+keyName, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != f.token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var req transitSignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		digest, err := base64.StdEncoding.DecodeString(req.Input)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Vault Transit's real sign response is a bare ASN.1 DER (r,s) with
		// no recovery id — crypto/ecdsa.SignASN1 (stdlib, Go 1.20+) produces
		// exactly that shape, over the same secp256k1 curve go-ethereum's
		// *ecdsa.PrivateKey already carries.
		der, err := ecdsa.SignASN1(rand.Reader, f.key, digest)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := transitSignResponse{}
		resp.Data.Signature = fmt.Sprintf("vault:v1:%s", base64.StdEncoding.EncodeToString(der))
		_ = json.NewEncoder(w).Encode(resp)
	})
	return httptest.NewServer(mux), f
}

func TestVaultProviderAddressMatchesKey(t *testing.T) {
	srv, f := newFakeVaultTransit(t, "rwa-compliance", "test-token")
	defer srv.Close()

	p, err := Load("compliance", Config{
		Mode: ModeVault, VaultAddr: srv.URL, VaultToken: "test-token", VaultKeyPrefix: "rwa-",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	addr, err := p.Address(context.Background())
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	want := crypto.PubkeyToAddress(f.key.PublicKey)
	if addr != want {
		t.Errorf("Address = %s, want %s", addr.Hex(), want.Hex())
	}
}

func TestVaultProviderSignTxRecoversToItsOwnAddress(t *testing.T) {
	srv, f := newFakeVaultTransit(t, "rwa-relayer", "test-token")
	defer srv.Close()

	p, err := Load("relayer", Config{
		Mode: ModeVault, VaultAddr: srv.URL, VaultToken: "test-token", VaultKeyPrefix: "rwa-",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
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
	want := crypto.PubkeyToAddress(f.key.PublicKey)
	if recovered != want {
		t.Errorf("recovered sender = %s, want %s", recovered.Hex(), want.Hex())
	}
}

func TestVaultProviderRequiresConfig(t *testing.T) {
	if _, err := Load("compliance", Config{Mode: ModeVault}); err == nil {
		t.Fatal("expected an error with no VaultAddr/VaultToken configured")
	}
	if _, err := Load("compliance", Config{Mode: ModeVault, VaultAddr: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("expected an error with no VaultToken configured")
	}
}

func TestVaultProviderWrongTokenErrors(t *testing.T) {
	srv, _ := newFakeVaultTransit(t, "rwa-compliance", "correct-token")
	defer srv.Close()
	if _, err := Load("compliance", Config{
		Mode: ModeVault, VaultAddr: srv.URL, VaultToken: "wrong-token", VaultKeyPrefix: "rwa-",
	}); err == nil {
		t.Fatal("expected an error with the wrong Vault token")
	}
}

// TestVaultProviderCloseIsNoOp is the K2 regression test's Vault half:
// unlike the other three backends, Close has nothing to zero (Vault never
// hands this client a private key at all), so it must simply succeed
// without disturbing the Provider's cached public key/address.
func TestVaultProviderCloseIsNoOp(t *testing.T) {
	srv, f := newFakeVaultTransit(t, "rwa-compliance", "test-token")
	defer srv.Close()
	p, err := Load("compliance", Config{
		Mode: ModeVault, VaultAddr: srv.URL, VaultToken: "test-token", VaultKeyPrefix: "rwa-",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	addr, err := p.Address(context.Background())
	if err != nil {
		t.Fatalf("Address after Close: %v", err)
	}
	if want := crypto.PubkeyToAddress(f.key.PublicKey); addr != want {
		t.Errorf("Address after Close = %s, want %s (Close must not disturb cached state)", addr.Hex(), want.Hex())
	}
}
