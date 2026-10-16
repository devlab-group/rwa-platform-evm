package sales

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
)

// nodeErr is a JSON-RPC error object the fake node answers a call with
type nodeErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// httpStatus makes the fake node fail the HTTP request itself, as a proxy or rate limiter does
type httpStatus int

var (
	rateLimited    = nodeErr{Code: -32005, Message: "limit exceeded"}
	headerNotFound = nodeErr{Code: -32000, Message: "header not found"}
	// geth reverts without data as -32000 and with data as code 3
	gethEmptyRevert = nodeErr{Code: -32000, Message: "execution reverted"}
)

// fakeNode serves eth_call over real JSON-RPC, so failures reach the service as
// go-ethereum's own client error types rather than hand-made fakes. A method without an
// answer reverts without data, as a call to a missing function does.
type fakeNode struct {
	client  *blockchain.RPCClient
	calls   atomic.Int64
	mu      sync.Mutex
	answers map[common.Address]map[string]any
}

func newFakeNode(t *testing.T, answers map[common.Address]map[string]any) *fakeNode {
	t.Helper()
	n := &fakeNode{answers: answers}
	abis := newScripted(nil).abis
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		var call struct {
			To    common.Address `json:"to"`
			Input hexutil.Bytes  `json:"input"`
			Data  hexutil.Bytes  `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != "eth_call" || len(req.Params) == 0 ||
			json.Unmarshal(req.Params[0], &call) != nil {
			http.Error(w, "fake node: unexpected request", http.StatusBadRequest)
			return
		}
		n.calls.Add(1)
		input := call.Input
		if len(input) == 0 {
			input = call.Data
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		answer, packed := any(gethEmptyRevert), []byte(nil)
		for _, a := range abis {
			m, err := a.MethodById(input[:4])
			if err != nil {
				continue
			}
			n.mu.Lock()
			v, ok := n.answers[call.To][m.Name]
			n.mu.Unlock()
			if !ok {
				break
			}
			answer = v
			if vals, isTuple := v.([]any); isTuple {
				packed, err = m.Outputs.Pack(vals...)
			} else if _, isErr := v.(nodeErr); !isErr {
				if _, isStatus := v.(httpStatus); !isStatus {
					packed, err = m.Outputs.Pack(v)
				}
			}
			if err != nil {
				t.Errorf("fake node: pack %s: %v", m.Name, err)
			}
			break
		}
		switch v := answer.(type) {
		case httpStatus:
			http.Error(w, "fake node: refused", int(v))
			return
		case nodeErr:
			resp["error"] = v
		default:
			resp["result"] = hexutil.Encode(packed)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	client, err := blockchain.Dial(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	n.client = client
	return n
}

func (n *fakeNode) set(addr common.Address, method string, v any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.answers[addr][method] = v
}

func (n *fakeNode) service() *Service {
	return New(n.client, oVault, oQuote, memory.NewPurchaseRepository())
}

// Node errors on feed() must remain Unknown and retry; caching Fixed would suppress Oracle
// alerts and wrongly expose strategy role controls
func TestStrategyKind_NodeErrorOnTheFeedProbe_IsUnknownAndRetried(t *testing.T) {
	cases := []struct {
		name string
		fail any
	}{
		{"rate limit", rateLimited},
		{"header not found", headerNotFound},
		{"HTTP 429", httpStatus(http.StatusTooManyRequests)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// an oracle also serves purchasePricePerWholeToken, the Fixed fallback probe
			node := newFakeNode(t, map[common.Address]map[string]any{
				oStrategy: {"feed": tc.fail, "purchasePricePerWholeToken": big.NewInt(1_503_750)},
			})
			svc := node.service()
			if got := svc.StrategyKind(context.Background(), oStrategy); got != KindUnknown {
				t.Fatalf("StrategyKind with feed() failing on the node = %q, want %q", got, KindUnknown)
			}
			node.set(oStrategy, "feed", oFeed)
			if got := svc.StrategyKind(context.Background(), oStrategy); got != KindOracle {
				t.Errorf("StrategyKind once the node recovers = %q, want %q", got, KindOracle)
			}
		})
	}
}

// The narrower revert check must still recognise a real revert in the formats nodes use, or a
// FixedPriceStrategy would never be identified
func TestStrategyKind_RevertFormatsOfRealNodes_IdentifyAFixedStrategy(t *testing.T) {
	cases := []struct {
		name   string
		revert nodeErr
	}{
		{"geth without data", gethEmptyRevert},
		{"code 3 with empty data", nodeErr{Code: 3, Message: "execution reverted", Data: "0x"}},
		{"capitalised message", nodeErr{Code: -32000, Message: "Execution reverted", Data: "0x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := newFakeNode(t, map[common.Address]map[string]any{
				oStrategy: {"feed": tc.revert, "purchasePricePerWholeToken": big.NewInt(1)},
			})
			if got := node.service().StrategyKind(context.Background(), oStrategy); got != KindFixed {
				t.Errorf("StrategyKind with feed() reverting as %+v = %q, want %q", tc.revert, got, KindFixed)
			}
		})
	}
}

// evaluateOracleAlert raises price_feed_stale only when TransportFailure is false, so a node
// error must be a transport failure and a strategy revert must not
func TestOracle_LatestPriceFailure_SeparatesNodeErrorsFromStrategyReverts(t *testing.T) {
	stale := errSelector("StalePrice(uint256,uint256)", 1, 2).data
	cases := []struct {
		name          string
		fail          any
		wantError     string
		wantTransport bool
	}{
		{"StalePrice revert", nodeErr{Code: 3, Message: "execution reverted", Data: stale}, bindings.PriceErrStalePrice, false},
		{"empty revert", gethEmptyRevert, bindings.PriceErrReadFailed, false},
		{"rate limit", rateLimited, bindings.PriceErrReadFailed, true},
		{"header not found", headerNotFound, bindings.PriceErrReadFailed, true},
		{"HTTP 503", httpStatus(http.StatusServiceUnavailable), bindings.PriceErrReadFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := oracleAnswers()
			answers[oStrategy]["latestPrice"] = tc.fail
			info, err := newFakeNode(t, answers).service().Oracle(context.Background(), oStrategy)
			if err != nil {
				t.Fatal(err)
			}
			if info.Available || info.Error != tc.wantError || info.TransportFailure != tc.wantTransport {
				t.Errorf("Oracle() = available %v, error %q, transport %v; want false, %q, %v",
					info.Available, info.Error, info.TransportFailure, tc.wantError, tc.wantTransport)
			}
		})
	}
}
