package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/income"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
	"github.com/rwa-platform/server/internal/sales"
)

const incomeChain int64 = 31337

var (
	incToken   = common.HexToAddress("0x000000000000000000000000000000000000b001")
	incDistrib = common.HexToAddress("0x000000000000000000000000000000000000b007")
	incAlice   = common.HexToAddress("0x00000000000000000000000000000000000000a1")
	incBob     = common.HexToAddress("0x00000000000000000000000000000000000000b2")
)

// withIncome turns the test app's project into an Active rwa-v3 deployment with an income
// distributor, indexes a small Transfer history and attaches an income service
func withIncome(t *testing.T, env *testEnv) *ipfs.FakeClient {
	t.Helper()
	ctx := context.Background()
	repos := env.app.Repos
	p, err := repos.Projects.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Version = "rwa-v3"
	p.Addresses.Token = incToken.Hex()
	p.Addresses.IncomeDistributor = incDistrib.Hex()
	p.Addresses.QuoteToken = common.HexToAddress("0xaa01").Hex()
	p.Addresses.SupplyController = ""
	if err := repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{ChainID: incomeChain, Address: indexer.CheckpointAddress, LastBlock: 100}); err != nil {
		t.Fatal(err)
	}
	transfer := func(block uint64, from, to common.Address, v string) {
		t.Helper()
		if err := repos.ChainEvents.Create(ctx, &models.ChainEvent{
			ChainID: incomeChain, Address: incToken.Hex(), TxHash: fmt.Sprintf("0xt%d", block), BlockNumber: block, Name: "Transfer",
			Data: map[string]any{"from": from.Hex(), "to": to.Hex(), "value": v},
		}); err != nil {
			t.Fatal(err)
		}
	}
	transfer(1, common.Address{}, env.vaultAddr, "1000")
	transfer(2, env.vaultAddr, incAlice, "300")
	transfer(3, env.vaultAddr, incBob, "100")
	fake := ipfs.NewFakeClient()
	env.app.Income = income.New(incomeChain, repos, fake, 3)
	return fake
}

func prepareBody(recordBlock int64, total string, deadline time.Time) map[string]any {
	return map[string]any{"recordBlock": recordBlock, "total": total, "claimDeadline": deadline.Unix()}
}

func adminHeaders(env *testEnv, key string) map[string]string {
	return map[string]string{"Authorization": env.bearer, "Idempotency-Key": key}
}

func TestPrepareIncomeDistribution(t *testing.T) {
	env := setupTestApp(t)
	fake := withIncome(t, env)
	deadline := time.Now().Add(8 * 24 * time.Hour)

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "1000", deadline), adminHeaders(env, "prep-1"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got dto.IncomeDistributionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "Prepared" || got.Total != "1000" || got.RecordBlock != 50 || got.HolderCount != 2 || got.EligibleSupply != "400" || got.Distributed != "1000" {
		t.Errorf("response = %+v", got)
	}
	if got.DistributionID != "" || got.Claimed != "" || got.CreateTxHash != "" {
		t.Errorf("a Prepared distribution carries on-chain fields: %+v", got)
	}
	if !strings.HasPrefix(got.SnapshotURI, "ipfs://") || len(got.MerkleRoot) != 66 || len(got.SnapshotSha256) != 66 || got.PreparationID == "" {
		t.Errorf("identifiers = %+v", got)
	}
	if _, err := fake.Get(context.Background(), strings.TrimPrefix(got.SnapshotURI, "ipfs://")); err != nil {
		t.Errorf("snapshot not pinned: %v", err)
	}

	// the preparation is audit-logged
	logs, err := env.app.Repos.AuditLogs.List(context.Background(), "income", 20)
	if err != nil || len(logs) < 2 {
		t.Fatalf("audit entries = %d err=%v, want intent and result", len(logs), err)
	}

	// the same Idempotency-Key replays the stored response instead of preparing again
	again := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "1000", deadline), adminHeaders(env, "prep-1"))
	if again.Code != http.StatusCreated || again.Body.String() != w.Body.String() {
		t.Errorf("replay = %d %s", again.Code, again.Body.String())
	}
	if list, _ := env.app.Repos.IncomePreparations.List(context.Background()); len(list) != 1 {
		t.Errorf("an idempotent replay stored %d preparations", len(list))
	}

	// and it shows in the list
	lw := doJSON(t, env.router, http.MethodGet, "/api/v1/income/distributions", nil, map[string]string{"Authorization": env.bearer})
	var list []dto.IncomeDistributionResponse
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil || lw.Code != http.StatusOK || len(list) != 1 || list[0].PreparationID != got.PreparationID {
		t.Fatalf("list = %d %s", lw.Code, lw.Body.String())
	}
	if lw.Header().Get("X-Total-Count") != "1" {
		t.Errorf("X-Total-Count = %q", lw.Header().Get("X-Total-Count"))
	}
}

func TestPrepareIncomeDistributionErrors(t *testing.T) {
	deadline := time.Now().Add(8 * 24 * time.Hour)
	cases := []struct {
		name   string
		body   any
		setup  func(t *testing.T, env *testEnv)
		status int
		code   string
	}{
		{"zero total", prepareBody(50, "0", deadline), nil, 400, CodeInvalidRequest},
		{"negative total", prepareBody(50, "-1", deadline), nil, 400, CodeInvalidRequest},
		{"non numeric total", prepareBody(50, "lots", deadline), nil, 400, CodeInvalidRequest},
		{"missing total", map[string]any{"recordBlock": 50, "claimDeadline": deadline.Unix()}, nil, 400, CodeInvalidRequest},
		{"missing record block", map[string]any{"total": "5", "claimDeadline": deadline.Unix()}, nil, 400, CodeInvalidRequest},
		{"missing deadline", map[string]any{"recordBlock": 50, "total": "5"}, nil, 400, CodeInvalidRequest},
		{"negative record block", prepareBody(-1, "5", deadline), nil, 400, CodeInvalidRequest},
		{"numeric total is not a string", map[string]any{"recordBlock": 50, "total": 5, "claimDeadline": deadline.Unix()}, nil, 400, CodeInvalidRequest},
		{"deadline too soon", prepareBody(50, "5", time.Now().Add(7*24*time.Hour)), nil, 400, CodeClaimWindowTooShort},
		{"record block not final", prepareBody(99, "5", deadline), nil, 409, CodeRecordBlockNotFinal},
		{"rwa-v2 project", prepareBody(50, "5", deadline), func(t *testing.T, env *testEnv) {
			p, _ := env.app.Repos.Projects.Get(context.Background())
			p.Version = "rwa-v2"
			p.Addresses.IncomeDistributor = ""
			_ = env.app.Repos.Projects.Upsert(context.Background(), p)
		}, 409, CodeIncomeUnsupported},
		{"project not active", prepareBody(50, "5", deadline), func(t *testing.T, env *testEnv) {
			p, _ := env.app.Repos.Projects.Get(context.Background())
			p.Status = models.ProjectStatusVerifying
			_ = env.app.Repos.Projects.Upsert(context.Background(), p)
		}, 409, CodeIncomeUnsupported},
		{"nobody holds RWA", prepareBody(50, "5", deadline), func(t *testing.T, env *testEnv) {
			if _, err := env.app.Repos.ChainEvents.DeleteFromBlock(context.Background(), incomeChain, incToken.Hex(), 2); err != nil {
				t.Fatal(err)
			}
		}, 409, CodeNoEligibleHolders},
		{"transfer history missing", prepareBody(50, "5", deadline), func(t *testing.T, env *testEnv) {
			_ = env.app.Repos.ChainEvents.Create(context.Background(), &models.ChainEvent{
				ChainID: incomeChain, Address: incToken.Hex(), TxHash: "0xbad", BlockNumber: 4, Name: "Transfer",
				Data: map[string]any{"from": incAlice.Hex(), "to": incBob.Hex(), "value": "9999"},
			})
		}, 409, CodeTransferHistoryMissing},
		// 12,000 one-unit holders make a canonical snapshot of about 1.3 MB
		{"snapshot over one IPFS block", prepareBody(50, "1"+strings.Repeat("0", 30), deadline), func(t *testing.T, env *testEnv) {
			ctx := context.Background()
			add := func(block uint64, tx string, from, to common.Address, v string) {
				t.Helper()
				if err := env.app.Repos.ChainEvents.Create(ctx, &models.ChainEvent{
					ChainID: incomeChain, Address: incToken.Hex(), TxHash: tx, BlockNumber: block, Name: "Transfer",
					Data: map[string]any{"from": from.Hex(), "to": to.Hex(), "value": v},
				}); err != nil {
					t.Fatal(err)
				}
			}
			add(4, "0xmint", common.Address{}, env.vaultAddr, "12000")
			for i := 0; i < 12000; i++ {
				add(5, fmt.Sprintf("0xh%d", i), env.vaultAddr, common.BigToAddress(big.NewInt(int64(0x100000+i))), "1")
			}
		}, 409, CodeSnapshotTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestApp(t)
			withIncome(t, env)
			if tc.setup != nil {
				tc.setup(t, env)
			}
			w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", tc.body, adminHeaders(env, "k-"+tc.name))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			var e dto.ErrorResponse
			_ = json.Unmarshal(w.Body.Bytes(), &e)
			if e.Code != tc.code {
				t.Errorf("code = %q, want %q", e.Code, tc.code)
			}
			if list, _ := env.app.Repos.IncomePreparations.List(context.Background()); len(list) != 0 {
				t.Errorf("a rejected request stored %d preparations", len(list))
			}
		})
	}
}

func TestPrepareIncomeDistributionIPFSDown(t *testing.T) {
	env := setupTestApp(t)
	withIncome(t, env)
	env.app.Income = income.New(incomeChain, env.app.Repos, nil, 3)
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "5", time.Now().Add(8*24*time.Hour)), adminHeaders(env, "k-ipfs"))
	var e dto.ErrorResponse
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != http.StatusServiceUnavailable || e.Code != CodeIPFSUnavailable {
		t.Fatalf("status=%d code=%q body=%s", w.Code, e.Code, w.Body.String())
	}
}

func TestIncomeEndpointsAreNotConfiguredWithoutAService(t *testing.T) {
	env := setupTestApp(t)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/income/distributions"},
		{http.MethodPost, "/api/v1/income/distributions"},
		{http.MethodGet, "/api/v1/income/claims/" + incAlice.Hex()},
	} {
		w := doJSON(t, env.router, rt.method, rt.path, prepareBody(1, "1", time.Now()), adminHeaders(env, "k"))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", rt.method, rt.path, w.Code)
		}
	}
}

func TestIncomeAdminRoutesRequireAnAdmin(t *testing.T) {
	env := setupTestApp(t)
	withIncome(t, env)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/income/distributions"},
		{http.MethodPost, "/api/v1/income/distributions"},
	} {
		w := doJSON(t, env.router, rt.method, rt.path, prepareBody(50, "5", time.Now().Add(8*24*time.Hour)), map[string]string{"Idempotency-Key": "anon"})
		if w.Code != http.StatusForbidden {
			t.Errorf("anonymous %s %s = %d, want 403", rt.method, rt.path, w.Code)
		}
	}
	// the admin POST also insists on an Idempotency-Key
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "5", time.Now().Add(8*24*time.Hour)), map[string]string{"Authorization": env.bearer})
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST without Idempotency-Key = %d, want 400", w.Code)
	}
}

func TestListIncomeClaims(t *testing.T) {
	env := setupTestApp(t)
	withIncome(t, env)
	ctx := context.Background()
	deadline := time.Now().Add(8 * 24 * time.Hour)

	w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "1000", deadline), adminHeaders(env, "prep-claims"))
	var prep dto.IncomeDistributionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &prep)

	// the manager creates it on-chain; alice claims
	for _, e := range []*models.ChainEvent{
		{ChainID: incomeChain, Address: incDistrib.Hex(), TxHash: "0xcreate", BlockNumber: 60, Name: "DistributionCreated", Data: map[string]any{
			"id": "0", "merkleRoot": prep.MerkleRoot, "total": "1000", "recordBlock": "50", "claimDeadline": fmt.Sprint(deadline.Unix()),
			"snapshotURI": prep.SnapshotURI, "snapshotSha256": prep.SnapshotSha256, "caller": incAlice.Hex()}},
		{ChainID: incomeChain, Address: incDistrib.Hex(), TxHash: "0xclaim", BlockNumber: 61, Name: "IncomeClaimed", Data: map[string]any{
			"id": "0", "account": incAlice.Hex(), "amount": "750"}},
	} {
		if err := env.app.Repos.ChainEvents.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.app.Income.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	claims := func(address string) (int, []dto.IncomeClaimResponse) {
		w := doJSON(t, env.router, http.MethodGet, "/api/v1/income/claims/"+address, nil, nil)
		var out []dto.IncomeClaimResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, aliceClaims := claims(strings.ToLower(incAlice.Hex()))
	if code != http.StatusOK || len(aliceClaims) != 1 {
		t.Fatalf("alice: %d %+v", code, aliceClaims)
	}
	c := aliceClaims[0]
	if c.Status != "Claimed" || c.ClaimTxHash != "0xclaim" || c.Amount != "750" || c.DistributionID != "0" || c.Account != incAlice.Hex() {
		t.Errorf("alice claim = %+v", c)
	}
	if c.MerkleRoot != prep.MerkleRoot || c.SnapshotURI != prep.SnapshotURI || c.ClaimDeadline != deadline.Unix() {
		t.Errorf("claim echoes the wrong distribution fields: %+v", c)
	}
	for _, p := range c.Proof {
		if len(p) != 66 || !strings.HasPrefix(p, "0x") {
			t.Errorf("proof element %q is not 0x + 64 hex", p)
		}
	}

	_, bobClaims := claims(incBob.Hex())
	if len(bobClaims) != 1 || bobClaims[0].Status != "Claimable" || bobClaims[0].Amount != "250" {
		t.Errorf("bob = %+v", bobClaims)
	}
	if _, none := claims(common.HexToAddress("0x1234").Hex()); len(none) != 0 {
		t.Errorf("a wallet outside the snapshot got %+v", none)
	}
	// the response is a JSON array even when empty
	w2 := doJSON(t, env.router, http.MethodGet, "/api/v1/income/claims/"+common.HexToAddress("0x1234").Hex(), nil, nil)
	if strings.TrimSpace(w2.Body.String()) != "[]" {
		t.Errorf("empty body = %q, want []", w2.Body.String())
	}

	for _, bad := range []string{"0x123", "not-an-address", "1234567890123456789012345678901234567890", "0x" + strings.Repeat("g", 40), "0x" + strings.Repeat("a", 41)} {
		if code, _ := claims(bad); code != http.StatusBadRequest {
			t.Errorf("address %q = %d, want 400", bad, code)
		}
	}
}

func TestListIncomeDistributionsPaginates(t *testing.T) {
	env := setupTestApp(t)
	withIncome(t, env)
	deadline := time.Now().Add(8 * 24 * time.Hour)
	for i := 1; i <= 3; i++ {
		w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, fmt.Sprint(i*100), deadline), adminHeaders(env, fmt.Sprint("p", i)))
		if w.Code != http.StatusCreated {
			t.Fatalf("prepare %d: %d %s", i, w.Code, w.Body.String())
		}
		time.Sleep(2 * time.Millisecond) // distinct CreatedAt so newest-first is well defined
	}
	h := map[string]string{"Authorization": env.bearer}
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/income/distributions?limit=2", nil, h)
	var page []dto.IncomeDistributionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page) != 2 || page[0].Total != "300" || page[1].Total != "200" {
		t.Fatalf("first page = %+v", page)
	}
	next := w.Header().Get("X-Next-Cursor")
	if next == "" || w.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("cursor=%q total=%q", next, w.Header().Get("X-Total-Count"))
	}
	w = doJSON(t, env.router, http.MethodGet, "/api/v1/income/distributions?limit=2&cursor="+next, nil, h)
	page = nil
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page) != 1 || page[0].Total != "100" || w.Header().Get("X-Next-Cursor") != "" {
		t.Errorf("second page = %+v cursor=%q", page, w.Header().Get("X-Next-Cursor"))
	}
}

// callScript answers eth_calls from a function, so a test can fail them with revert data
type callScript struct {
	*blockchain.FakeClient
	fn func(msg ethereum.CallMsg) ([]byte, error)
}

func (c callScript) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	return c.fn(msg)
}

type apiRevert struct{ data string }

func (e apiRevert) Error() string          { return "execution reverted" }
func (e apiRevert) ErrorData() interface{} { return e.data }

// oracleClient makes every strategy view of an OraclePriceStrategy answer, with the price
// views failing with StalePrice when stale is set
func oracleClient(t *testing.T, feed common.Address, stale bool) callScript {
	t.Helper()
	oracle := bindings.NewOraclePriceStrategy()
	agg := bindings.NewAggregatorV3()
	vault := bindings.NewVault()
	staleID := oracle.ABI.Errors["StalePrice"].ID
	stalePrice := apiRevert{data: "0x" + common.Bytes2Hex(staleID[:4]) + strings.Repeat("00", 64)}
	return callScript{FakeClient: blockchain.NewFakeClient(), fn: func(msg ethereum.CallMsg) ([]byte, error) {
		if m, err := oracle.ABI.MethodById(msg.Data[:4]); err == nil && *msg.To != feed {
			switch m.Name {
			case "feed":
				return m.Outputs.Pack(feed)
			case "sequencerUptimeFeed":
				return m.Outputs.Pack(common.Address{})
			case "maxAge", "gracePeriod":
				return m.Outputs.Pack(map[string]uint64{"maxAge": 3600, "gracePeriod": 0}[m.Name])
			case "purchaseSpreadBps", "redemptionSpreadBps":
				return m.Outputs.Pack(uint16(25))
			case "feedDecimals":
				return m.Outputs.Pack(uint8(8))
			case "latestPrice":
				if stale {
					return nil, stalePrice
				}
				return m.Outputs.Pack(big.NewInt(150_000_000), big.NewInt(1_700_000_000))
			case "purchasePricePerWholeToken", "redemptionPricePerWholeToken":
				if stale {
					return nil, stalePrice
				}
				return m.Outputs.Pack(big.NewInt(1_500_000))
			}
		}
		if m, err := agg.ABI.MethodById(msg.Data[:4]); err == nil && *msg.To == feed {
			return m.Outputs.Pack(big.NewInt(1), big.NewInt(150_000_000), big.NewInt(1), big.NewInt(1_700_000_000), big.NewInt(1))
		}
		if m, err := vault.ABI.MethodById(msg.Data[:4]); err == nil && m.Name == "strategy" {
			return m.Outputs.Pack(common.HexToAddress("0x000000000000000000000000000000000000c0c0"))
		}
		return nil, apiRevert{data: "0x"}
	}}
}

func TestProjectResponseReportsAnOracleStrategy(t *testing.T) {
	feed := common.HexToAddress("0x000000000000000000000000000000000000feed")
	strategy := common.HexToAddress("0x000000000000000000000000000000000000c0c0")
	for _, tc := range []struct {
		name      string
		stale     bool
		available bool
		price     string
		err       string
	}{
		{"fresh", false, true, "1500000", ""},
		{"stale", true, false, "", "StalePrice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestApp(t)
			ctx := context.Background()
			p, _ := env.app.Repos.Projects.Get(ctx)
			p.Addresses.Strategy = strategy.Hex()
			// stale projected prices must not leak through when the oracle read fails
			p.Security = &models.SecurityState{PurchasePricePerWholeToken: "777", RedemptionPricePerWholeToken: "666", MinPurchaseQuote: "42"}
			p.MinPurchaseQuote = "1"
			_ = env.app.Repos.Projects.Upsert(ctx, p)
			env.app.Sales = sales.New(oracleClient(t, feed, tc.stale), env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

			resp := getProjectResp(t, env)
			if resp.StrategyKind != "Oracle" || resp.Oracle == nil {
				t.Fatalf("strategyKind=%q oracle=%+v", resp.StrategyKind, resp.Oracle)
			}
			o := resp.Oracle
			if o.Feed != feed.Hex() || o.MaxAge != 3600 || o.PurchaseSpreadBps != 25 || o.FeedDecimals != 8 || o.Available != tc.available || o.Error != tc.err {
				t.Errorf("oracle = %+v", o)
			}
			if o.SequencerUptimeFeed != "" || o.GracePeriod != 0 {
				t.Errorf("no sequencer feed is configured, got %q / %d", o.SequencerUptimeFeed, o.GracePeriod)
			}
			if o.UpdatedAt == nil || *o.UpdatedAt != 1_700_000_000 {
				t.Errorf("updatedAt = %v (reported also when stale)", o.UpdatedAt)
			}
			if resp.PurchasePricePerWholeToken != tc.price || resp.RedemptionPricePerWholeToken != tc.price {
				t.Errorf("prices = %q / %q, want %q", resp.PurchasePricePerWholeToken, resp.RedemptionPricePerWholeToken, tc.price)
			}
			if resp.MinPurchaseQuote != "42" {
				t.Errorf("minPurchaseQuote = %q, want the projected 42", resp.MinPurchaseQuote)
			}
		})
	}
}

func TestProjectResponseFixedStrategyKeepsProjectedPrices(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, _ := env.app.Repos.Projects.Get(ctx)
	p.Addresses.Strategy = common.HexToAddress("0x000000000000000000000000000000000000c0c0").Hex()
	p.Security = &models.SecurityState{PurchasePricePerWholeToken: "1500", RedemptionPricePerWholeToken: "1400"}
	_ = env.app.Repos.Projects.Upsert(ctx, p)
	fixed := blockchain.NewFakeClient()
	strategyABI := bindings.NewFixedPriceStrategy()
	data, _ := strategyABI.PackPurchasePricePerWholeToken()
	out, _ := strategyABI.ABI.Methods["purchasePricePerWholeToken"].Outputs.Pack(big.NewInt(1500))
	fixed.CallResponses[common.Bytes2Hex(data)] = out
	env.app.Sales = sales.New(callScript{FakeClient: fixed, fn: func(msg ethereum.CallMsg) ([]byte, error) {
		if out, ok := fixed.CallResponses[common.Bytes2Hex(msg.Data)]; ok {
			return out, nil
		}
		return nil, apiRevert{data: "0x"} // feed() reverts: not an oracle
	}}, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	resp := getProjectResp(t, env)
	if resp.StrategyKind != "Fixed" || resp.Oracle != nil {
		t.Errorf("strategyKind=%q oracle=%+v", resp.StrategyKind, resp.Oracle)
	}
	if resp.PurchasePricePerWholeToken != "1500" || resp.RedemptionPricePerWholeToken != "1400" {
		t.Errorf("prices = %q / %q", resp.PurchasePricePerWholeToken, resp.RedemptionPricePerWholeToken)
	}
	if resp.MinPurchaseQuote != "" || resp.Addresses.IncomeDistributor != "" {
		t.Errorf("a v2 project reported v3 fields: min=%q income=%q", resp.MinPurchaseQuote, resp.Addresses.IncomeDistributor)
	}
}

func TestProjectResponseV3Fields(t *testing.T) {
	env := setupTestApp(t)
	withIncome(t, env)
	ctx := context.Background()
	p, _ := env.app.Repos.Projects.Get(ctx)
	p.MinPurchaseQuote = "5000000"
	_ = env.app.Repos.Projects.Upsert(ctx, p)
	resp := getProjectResp(t, env)
	if resp.Addresses.IncomeDistributor != incDistrib.Hex() || resp.MinPurchaseQuote != "5000000" {
		t.Errorf("income=%q min=%q", resp.Addresses.IncomeDistributor, resp.MinPurchaseQuote)
	}
}

func TestInventoryReportsPriceErrorForAStaleOracle(t *testing.T) {
	env := setupTestApp(t)
	feed := common.HexToAddress("0x000000000000000000000000000000000000feed")
	c := oracleClient(t, feed, true)
	inner := c.fn
	erc20, vaultABI := bindings.NewERC20().ABI, bindings.NewVault().ABI
	c.fn = func(msg ethereum.CallMsg) ([]byte, error) {
		if m, err := erc20.MethodById(msg.Data[:4]); err == nil && m.Name == "balanceOf" {
			return m.Outputs.Pack(big.NewInt(900))
		}
		if m, err := vaultABI.MethodById(msg.Data[:4]); err == nil && m.Name == "inventory" {
			return m.Outputs.Pack(big.NewInt(500))
		}
		return inner(msg)
	}
	env.app.Sales = sales.New(c, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/inventory", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["priceError"] != "StalePrice" || got["inventory"] != "500" {
		t.Errorf("body = %v", got)
	}
	if _, has := got["purchasePrice"]; has {
		t.Errorf("purchasePrice must be omitted: %v", got)
	}
}

func TestConfigReportsTheFactoryVersion(t *testing.T) {
	env := setupTestApp(t)
	// no project service: nothing can be read, so the version is empty but present
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/config", nil, nil)
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if v, has := raw["factoryVersion"]; !has || v != "" {
		t.Errorf("factoryVersion = %v (present=%v), want an empty string", v, has)
	}
}

func TestPrepareIncomeDistributionAlreadyOnChain(t *testing.T) {
	env := setupTestApp(t)
	withIncome(t, env)
	deadline := time.Now().Add(8 * 24 * time.Hour)
	w := doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "1000", deadline), adminHeaders(env, "dup-1"))
	var got dto.IncomeDistributionResponse
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("first prepare = %d: %s", w.Code, w.Body.String())
	}
	if err := env.app.Repos.IncomeDistributions.Upsert(context.Background(), &models.IncomeDistribution{
		ID: "0", MerkleRoot: got.MerkleRoot, SnapshotSha256: got.SnapshotSha256, ClaimDeadline: deadline.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, env.router, http.MethodPost, "/api/v1/income/distributions", prepareBody(50, "1000", deadline.Add(24*time.Hour)), adminHeaders(env, "dup-2"))
	var e dto.ErrorResponse
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != http.StatusConflict || e.Code != CodeAlreadyDistributed {
		t.Fatalf("status = %d code = %q, want 409 %s", w.Code, e.Code, CodeAlreadyDistributed)
	}
}

func TestProjectResponseKeepsStoredKindWhenTheProbeFails(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, _ := env.app.Repos.Projects.Get(ctx)
	p.Addresses.Strategy = common.HexToAddress("0x000000000000000000000000000000000000c0c0").Hex()
	p.StrategyKind = models.StrategyKindFixed
	_ = env.app.Repos.Projects.Upsert(ctx, p)
	down := true
	calls := 0
	env.app.Sales = sales.New(callScript{FakeClient: blockchain.NewFakeClient(), fn: func(ethereum.CallMsg) ([]byte, error) {
		calls++
		if down {
			return nil, errors.New("rpc unreachable")
		}
		return nil, apiRevert{data: "0x"} // feed() reverts: not an oracle
	}}, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	if resp := getProjectResp(t, env); resp.StrategyKind != "Fixed" {
		t.Errorf("strategyKind during an RPC outage = %q, want the stored Fixed", resp.StrategyKind)
	}
	// an outage result is cached too, so public traffic does not probe the chain per request
	before := calls
	getProjectResp(t, env)
	if calls != before {
		t.Errorf("an outage result was re-probed within the TTL (%d new calls)", calls-before)
	}
	// once the entry expires the next request probes again and sees the recovered chain
	addr := common.HexToAddress(p.Addresses.Strategy)
	env.app.pricing.mu.Lock()
	hit := env.app.pricing.m[addr]
	hit.expires = time.Now().Add(-time.Second)
	env.app.pricing.m[addr] = hit
	env.app.pricing.mu.Unlock()
	down = false
	if resp := getProjectResp(t, env); calls == before || resp.StrategyKind != "Fixed" {
		t.Errorf("after expiry: calls %d -> %d, strategyKind %q", before, calls, resp.StrategyKind)
	}
}

func TestProjectResponseCachesLivePricing(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, _ := env.app.Repos.Projects.Get(ctx)
	p.Addresses.Strategy = common.HexToAddress("0x000000000000000000000000000000000000c0c0").Hex()
	_ = env.app.Repos.Projects.Upsert(ctx, p)
	inner := oracleClient(t, common.HexToAddress("0x000000000000000000000000000000000000feed"), false)
	calls := 0
	env.app.Sales = sales.New(callScript{FakeClient: inner.FakeClient, fn: func(msg ethereum.CallMsg) ([]byte, error) {
		calls++
		return inner.fn(msg)
	}}, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	first := getProjectResp(t, env)
	after := calls
	second := getProjectResp(t, env)
	if calls != after || first.PurchasePricePerWholeToken == "" || second.PurchasePricePerWholeToken != first.PurchasePricePerWholeToken || second.Oracle == nil {
		t.Errorf("second request made %d extra calls, response %+v", calls-after, second)
	}
}

// ctxScript fails a call once its context is done, as a real RPC client does
type ctxScript struct{ callScript }

func (c ctxScript) CallContract(ctx context.Context, msg ethereum.CallMsg, n *big.Int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.callScript.CallContract(ctx, msg, n)
}

// A client that disconnects must not cache a failed read: the shared result is read on a
// context detached from that request
func TestProjectResponseIgnoresACancelledRequestForTheCache(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, _ := env.app.Repos.Projects.Get(ctx)
	p.Addresses.Strategy = common.HexToAddress("0x000000000000000000000000000000000000c0c0").Hex()
	_ = env.app.Repos.Projects.Upsert(ctx, p)
	inner := oracleClient(t, common.HexToAddress("0x000000000000000000000000000000000000feed"), false)
	env.app.Sales = sales.New(ctxScript{inner}, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/project", nil).WithContext(cancelled)
	env.router.ServeHTTP(httptest.NewRecorder(), req)

	resp := getProjectResp(t, env)
	if resp.StrategyKind != "Oracle" || resp.Oracle == nil || resp.PurchasePricePerWholeToken != "1500000" {
		t.Errorf("after a cancelled request: strategyKind=%q oracle=%+v price=%q", resp.StrategyKind, resp.Oracle, resp.PurchasePricePerWholeToken)
	}
}

// Concurrent GET /project misses for one strategy share a single set of chain reads instead
// of each probing the chain
func TestProjectResponseCollapsesConcurrentPricingMisses(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	p, _ := env.app.Repos.Projects.Get(ctx)
	p.Addresses.Strategy = common.HexToAddress("0x000000000000000000000000000000000000c0c0").Hex()
	_ = env.app.Repos.Projects.Upsert(ctx, p)
	inner := oracleClient(t, common.HexToAddress("0x000000000000000000000000000000000000feed"), false)
	oracle := bindings.NewOraclePriceStrategy()
	var release chan struct{}
	var priceReads atomic.Int32
	env.app.Sales = sales.New(callScript{FakeClient: inner.FakeClient, fn: func(msg ethereum.CallMsg) ([]byte, error) {
		if m, err := oracle.ABI.MethodById(msg.Data[:4]); err == nil {
			switch m.Name {
			case "feed":
				<-release // hold the strategy probe until every request is in flight
			case "latestPrice":
				priceReads.Add(1)
			}
		}
		return inner.fn(msg)
	}}, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	const n = 5
	synctest.Test(t, func(t *testing.T) {
		release = make(chan struct{})
		codes := make([]int, n)
		bodies := make([]dto.ProjectResponse, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				w := httptest.NewRecorder()
				env.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/project", nil))
				codes[i] = w.Code
				_ = json.Unmarshal(w.Body.Bytes(), &bodies[i])
			})
		}
		synctest.Wait() // one request holds the probe, the others wait on its result
		close(release)
		wg.Wait()

		if got := priceReads.Load(); got != 1 {
			t.Errorf("%d concurrent misses made %d latestPrice reads, want 1", n, got)
		}
		for i := range n {
			if codes[i] != http.StatusOK || bodies[i].Oracle == nil || bodies[i].PurchasePricePerWholeToken != "1500000" {
				t.Errorf("request %d: status %d oracle %+v price %q", i, codes[i], bodies[i].Oracle, bodies[i].PurchasePricePerWholeToken)
			}
		}
	})
}
