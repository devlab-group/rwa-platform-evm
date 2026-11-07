package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/keeper"
)

// fakeRPC answers eth_chainId with 0x7a69 (31337) and fails everything else
func fakeRPC(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "eth_chainId" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x7a69"}`, req.ID)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"not served"}}`, req.ID)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func writeConfig(t *testing.T, rpcURL string, chainID int, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keeper.yaml")
	body := fmt.Sprintf(`
rpc_url: %q
chain_id: %d
vault_address: "0x00000000000000000000000000000000000000aa"
keys:
  keeper_key: %q
`, rpcURL, chainID, key)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const testKey = "0x2a871d0798f97d79848a013d4936a73bf4cc922c825d33c1cf7073dff6d409c6"

func TestRun_RefusesToStart(t *testing.T) {
	rpc := fakeRPC(t)
	for name, tc := range map[string]struct{ path, want string }{
		"no config flag":   {"", "--config"},
		"invalid config":   {filepath.Join(t.TempDir(), "absent.yaml"), "config:"},
		"wrong chain":      {writeConfig(t, rpc, 1, testKey), "RPC endpoint is on 31337"},
		"no key":           {writeConfig(t, rpc, 31337, ""), "no key configured"},
		"unreadable Vault": {writeConfig(t, rpc, 31337, testKey), "nextPurchaseId"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(tc.path, true)
			if err == nil {
				t.Fatal("run succeeded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// callFails answers every eth_call with err, or with no data when err is nil, as an address
// without code does
type callFails struct {
	*blockchain.FakeClient
	err error
}

func (c callFails) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return nil, c.err
}

func TestFirstPass_OnlyUnfixableFailuresStopStartup(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	vault := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	transient := errors.New("connection refused")
	for name, tc := range map[string]struct {
		callErr error
		once    bool
		wantErr bool
	}{
		"transient failure, long-running": {callErr: transient},
		"transient failure, one shot":     {callErr: transient, once: true, wantErr: true},
		"no such Vault, long-running":     {wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			client := callFails{FakeClient: blockchain.NewFakeClient(), err: tc.callErr}
			k := keeper.New(client, blockchain.NewStaticKeySigner(key), big.NewInt(31337), vault, blockchain.FeeCaps{})

			err := firstPass(context.Background(), k, tc.once)

			if (err != nil) != tc.wantErr {
				t.Fatalf("firstPass error = %v, want an error: %v", err, tc.wantErr)
			}
		})
	}
}
