package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/sales"
)

func seedOrder(t *testing.T, env *testEnv, o models.PurchaseOrder) {
	t.Helper()
	if o.Buyer == "" {
		o.Buyer = addr("0xAAAA")
	}
	o.Recipient, o.TokenAmount, o.MaxQuoteAmount, o.Strategy = addr("0xBBBB"), "1000", "2500", addr("0x5757")
	o.BaseRound, o.RequestTxHash, o.UpdatedAt = "36893488147419103232", "0xreq-"+o.ID, time.Now().UTC()
	if o.Status == "" {
		o.Status = models.PurchaseOrderPending
	}
	if err := env.app.Repos.PurchaseOrders.Upsert(context.Background(), &o); err != nil {
		t.Fatal(err)
	}
}

func listOrders(t *testing.T, env *testEnv, query string) (*httpResult, []dto.PurchaseOrderResponse) {
	t.Helper()
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/orders"+query, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /sales/orders%s = %d %s", query, w.Code, w.Body.String())
	}
	var out []dto.PurchaseOrderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return &httpResult{header: w.Header(), body: w.Body.String()}, out
}

type httpResult struct {
	header http.Header
	body   string
}

func TestListPurchaseOrdersIsPublicAndFiltersByStatusAndBuyer(t *testing.T) {
	env := setupTestApp(t)
	alice, bob := addr("0xAAAA"), addr("0xBBB1")
	seedOrder(t, env, models.PurchaseOrder{ID: "1", Buyer: alice, Status: models.PurchaseOrderPending, CreatedAt: 10})
	seedOrder(t, env, models.PurchaseOrder{ID: "2", Buyer: bob, Status: models.PurchaseOrderSettled, CreatedAt: 20})
	seedOrder(t, env, models.PurchaseOrder{ID: "3", Buyer: alice, Status: models.PurchaseOrderSettled, CreatedAt: 30})

	ids := func(query string) string {
		_, got := listOrders(t, env, query) // no Authorization header: the endpoint is public
		out := make([]string, len(got))
		for i, o := range got {
			out[i] = o.ID
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ query, want string }{
		{"", "3,2,1"},
		{"?status=Settled", "3,2"},
		{"?address=" + strings.ToLower(alice), "3,1"},
		{"?address=" + alice + "&status=Settled", "3"},
		{"?status=Rejected", ""},
		{"?address=" + addr("0xDEAD"), ""},
	} {
		if got := ids(tc.query); got != tc.want {
			t.Errorf("GET /sales/orders%s = [%s], want [%s]", tc.query, got, tc.want)
		}
	}

	// an empty result is an empty array, not null
	res, _ := listOrders(t, env, "?status=Rejected")
	if strings.TrimSpace(res.body) != "[]" {
		t.Errorf("empty list body = %q, want []", res.body)
	}
}

func TestListPurchaseOrdersPaginatesWithKeysetHeaders(t *testing.T) {
	env := setupTestApp(t)
	for i := 1; i <= 7; i++ {
		seedOrder(t, env, models.PurchaseOrder{ID: fmt.Sprintf("o-%d", i), CreatedAt: int64(i)})
	}

	var walked []string
	query := "?limit=3"
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatal("pagination did not terminate")
		}
		res, items := listOrders(t, env, query)
		if res.header.Get("X-Total-Count") != "" {
			t.Errorf("X-Total-Count = %q, want it omitted (a keyset page has no count)", res.header.Get("X-Total-Count"))
		}
		if res.header.Get("X-Page-Size") != strconv.Itoa(len(items)) {
			t.Errorf("X-Page-Size = %q, want %d", res.header.Get("X-Page-Size"), len(items))
		}
		for _, o := range items {
			walked = append(walked, o.ID)
		}
		next := res.header.Get("X-Next-Cursor")
		if next == "" {
			break
		}
		query = "?limit=3&cursor=" + next
	}
	if got := strings.Join(walked, ","); got != "o-7,o-6,o-5,o-4,o-3,o-2,o-1" {
		t.Errorf("walked %s, want every order once, newest first", got)
	}
}

func TestListPurchaseOrdersToleratesInvalidPaging(t *testing.T) {
	env := setupTestApp(t)
	for i := 1; i <= 3; i++ {
		seedOrder(t, env, models.PurchaseOrder{ID: fmt.Sprintf("o-%d", i), CreatedAt: int64(i)})
	}
	for _, query := range []string{"?limit=abc", "?limit=0", "?limit=-5", "?limit=100000", "?cursor=not-a-cursor", "?cursor=%00"} {
		_, got := listOrders(t, env, query)
		if len(got) != 3 {
			t.Errorf("GET /sales/orders%s returned %d orders, want all 3 from the first page", query, len(got))
		}
	}
}

func TestGetPurchaseOrderShapesPendingAndSettledOrders(t *testing.T) {
	env := setupTestApp(t)
	seedOrder(t, env, models.PurchaseOrder{ID: "1", CreatedAt: 1000, TimeoutAt: 1000 + 604800})
	seedOrder(t, env, models.PurchaseOrder{
		ID:          "2",
		Status:      models.PurchaseOrderSettled,
		CreatedAt:   2000,
		TimeoutAt:   2000 + 604800,
		PricedRound: "36893488147419103233",
		QuoteAmount: "2400",
		Refund:      "100",
		CloseTxHash: "0xclose",
	})

	get := func(id string) map[string]any {
		w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/orders/"+id, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /sales/orders/%s = %d %s", id, w.Code, w.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	pending := get("1")
	// the required members of components.schemas.PurchaseOrder
	for _, k := range []string{"id", "buyer", "recipient", "tokenAmount", "maxQuoteAmount", "strategy", "baseRound", "status", "createdAt", "timeoutAt", "requestTxHash"} {
		if _, ok := pending[k]; !ok {
			t.Errorf("pending order lacks required member %q: %v", k, pending)
		}
	}
	for _, k := range []string{"pricedRound", "quoteAmount", "refund", "closeTxHash"} {
		if _, ok := pending[k]; ok {
			t.Errorf("pending order carries %q = %v, want it omitted", k, pending[k])
		}
	}
	if pending["status"] != "Pending" || pending["timeoutAt"] != float64(1000+604800) || pending["baseRound"] != "36893488147419103232" {
		t.Errorf("pending order = %v", pending)
	}

	settled := get("2")
	for k, want := range map[string]any{
		"status":      "Settled",
		"pricedRound": "36893488147419103233",
		"quoteAmount": "2400",
		"refund":      "100",
		"closeTxHash": "0xclose",
	} {
		if settled[k] != want {
			t.Errorf("settled order %s = %v, want %v", k, settled[k], want)
		}
	}

	// every member the response can carry is a member of the schema
	allowed := map[string]bool{}
	for _, k := range []string{"id", "buyer", "recipient", "tokenAmount", "maxQuoteAmount", "strategy", "baseRound", "status", "createdAt", "timeoutAt", "pricedRound", "quoteAmount", "refund", "requestTxHash", "closeTxHash"} {
		allowed[k] = true
	}
	var extra []string
	for k := range settled {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("response carries members outside components.schemas.PurchaseOrder: %v", extra)
	}
}

func TestGetPurchaseOrderUnknownIDIsNotFound(t *testing.T) {
	env := setupTestApp(t)
	w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/orders/404", nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var e dto.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != CodeNotFound {
		t.Errorf("error = %+v (%v), want code %q", e, err, CodeNotFound)
	}
}

func TestPurchaseOrderEndpointsAnswer501WhenNotConfigured(t *testing.T) {
	for name, configure := range map[string]func(env *testEnv){
		"no sales service": func(env *testEnv) { env.app.Sales = nil },
		"sales without order repo": func(env *testEnv) {
			env.app.Sales = sales.New(env.client, env.vaultAddr, common.Address{}, env.app.Repos.Purchases)
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := setupTestApp(t)
			configure(env)
			for _, path := range []string{"/api/v1/sales/orders", "/api/v1/sales/orders/1"} {
				w := doJSON(t, env.router, http.MethodGet, path, nil, nil)
				if w.Code != http.StatusNotImplemented {
					t.Errorf("GET %s = %d %s, want 501", path, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestRedemptionResponseCarriesForwardPricingFields(t *testing.T) {
	env := setupTestApp(t)
	ctx := context.Background()
	if err := env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID:          "1",
		Beneficiary: addr("0xB0B0"),
		RWAAmount:   "1000",
		QuoteAmount: "0",
		Status:      models.RedemptionPending,
		CreatedAt:   1000,
		TimeoutAt:   2000,
		Strategy:    addr("0x5757"),
		BaseRound:   "36893488147419103232",
		MinQuoteOut: "900",
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID:            "2",
		Beneficiary:   addr("0xB0B0"),
		RWAAmount:     "1000",
		QuoteAmount:   "950",
		Status:        models.RedemptionFunded,
		FundedAtBlock: 95,
		CreatedAt:     1000,
		TimeoutAt:     2000,
		Strategy:      addr("0x5757"),
		BaseRound:     "36893488147419103232",
		MinQuoteOut:   "900",
		PricedRound:   "36893488147419103233",
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Repos.RedemptionRequests.Upsert(ctx, &models.RedemptionRequest{
		ID:          "3",
		Beneficiary: addr("0xB0B0"),
		RWAAmount:   "1000",
		QuoteAmount: "950",
		Status:      models.RedemptionPending,
		CreatedAt:   1000,
		TimeoutAt:   2000,
	}); err != nil {
		t.Fatal(err)
	}
	get := func(id string) map[string]any {
		w := doJSON(t, env.router, http.MethodGet, "/api/v1/redemptions/"+id, nil, nil)
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	pending := get("1")
	if pending["quoteAmount"] != "0" || pending["baseRound"] != "36893488147419103232" || pending["minQuoteOut"] != "900" || pending["strategy"] != addr("0x5757") {
		t.Errorf("pending forward request = %v", pending)
	}
	if _, ok := pending["pricedRound"]; ok {
		t.Errorf("an unfunded forward request carries pricedRound %v", pending["pricedRound"])
	}
	funded := get("2")
	if funded["quoteAmount"] != "950" || funded["pricedRound"] != "36893488147419103233" || funded["claimable"] != true {
		t.Errorf("funded forward request = %v", funded)
	}
	instant := get("3")
	for _, k := range []string{"strategy", "baseRound", "minQuoteOut", "pricedRound"} {
		if _, ok := instant[k]; ok {
			t.Errorf("a request priced at request time carries %q = %v", k, instant[k])
		}
	}
}

// v4Script layers the rwa-v4 views over an rwa-v3 oracleClient
func v4Script(base callScript, vaultPending *big.Int, cutoff uint64, forward func() ([]byte, error)) callScript {
	oracle, vault := bindings.NewOraclePriceStrategy(), bindings.NewVault()
	return callScript{
		FakeClient: base.FakeClient,
		fn: func(msg ethereum.CallMsg) ([]byte, error) {
			if m, err := oracle.ABI.MethodById(msg.Data[:4]); err == nil {
				switch m.Name {
				case "dealingCutoff":
					return m.Outputs.Pack(cutoff)
				case "forwardPriced":
					return forward()
				}
			}
			if m, err := vault.ABI.MethodById(msg.Data[:4]); err == nil && m.Name == "pendingPurchaseQuote" {
				return m.Outputs.Pack(vaultPending)
			}
			return base.fn(msg)
		},
	}
}

func TestProjectResponseReportsForwardPricingForRwaV4Only(t *testing.T) {
	feed := common.HexToAddress("0x000000000000000000000000000000000000feed")
	strategy := common.HexToAddress("0x000000000000000000000000000000000000c0c0")
	forwardTrue := func() ([]byte, error) {
		return bindings.NewOraclePriceStrategy().ABI.Methods["forwardPriced"].Outputs.Pack(true)
	}
	forwardRPCFailure := func() ([]byte, error) { return nil, fmt.Errorf("i/o timeout") }

	setup := func(t *testing.T, version string, forward func() ([]byte, error)) *testEnv {
		t.Helper()
		env := setupTestApp(t)
		ctx := context.Background()
		p, _ := env.app.Repos.Projects.Get(ctx)
		p.Version, p.Addresses.Strategy = version, strategy.Hex()
		if err := env.app.Repos.Projects.Upsert(ctx, p); err != nil {
			t.Fatal(err)
		}
		env.app.Sales = sales.New(v4Script(oracleClient(t, feed, false), big.NewInt(0), 7200, forward), env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)
		return env
	}
	raw := func(t *testing.T, env *testEnv) map[string]any {
		t.Helper()
		w := doJSON(t, env.router, http.MethodGet, "/api/v1/project", nil, nil)
		var m map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	t.Run("rwa-v4 reads forwardPriced and the cut-off from the live strategy", func(t *testing.T) {
		m := raw(t, setup(t, "rwa-v4", forwardTrue))
		oracle, _ := m["oracle"].(map[string]any)
		if m["forwardPriced"] != true || oracle["dealingCutoff"] != float64(7200) {
			t.Errorf("forwardPriced=%v oracle.dealingCutoff=%v, want true and 7200", m["forwardPriced"], oracle["dealingCutoff"])
		}
	})
	t.Run("rwa-v4 omits forwardPriced when the read fails", func(t *testing.T) {
		m := raw(t, setup(t, "rwa-v4", forwardRPCFailure))
		if _, ok := m["forwardPriced"]; ok {
			t.Errorf("forwardPriced = %v after a failed read, want it omitted", m["forwardPriced"])
		}
		if m["strategyKind"] != "Oracle" {
			t.Errorf("a failed forwardPriced read must not hide the rest of the pricing: %v", m["strategyKind"])
		}
	})
	t.Run("rwa-v3 never reads forwardPriced", func(t *testing.T) {
		reads := 0
		env := setup(t, "rwa-v3", func() ([]byte, error) { reads++; return forwardTrue() })
		m := raw(t, env)
		if _, ok := m["forwardPriced"]; ok || reads != 0 {
			t.Errorf("an rwa-v3 project reports forwardPriced = %v after %d reads, want it omitted and never read", m["forwardPriced"], reads)
		}
		// the cut-off is a property of the strategy, not of the project version: a strategy
		// that has it reports it
		if oracle, _ := m["oracle"].(map[string]any); oracle["dealingCutoff"] != float64(7200) {
			t.Errorf("oracle.dealingCutoff = %v", oracle["dealingCutoff"])
		}
	})
}

func TestInventoryReportsPendingPurchaseQuote(t *testing.T) {
	feed := common.HexToAddress("0x000000000000000000000000000000000000feed")
	env := setupTestApp(t)
	vault, erc20 := bindings.NewVault(), bindings.NewERC20()
	script := v4Script(oracleClient(t, feed, false), big.NewInt(123456), 7200, nil)
	inner := script.fn
	script.fn = func(msg ethereum.CallMsg) ([]byte, error) {
		if m, err := vault.ABI.MethodById(msg.Data[:4]); err == nil && m.Name == "inventory" {
			return m.Outputs.Pack(big.NewInt(500))
		}
		if m, err := erc20.ABI.MethodById(msg.Data[:4]); err == nil && m.Name == "balanceOf" {
			return m.Outputs.Pack(big.NewInt(900))
		}
		return inner(msg)
	}
	env.app.Sales = sales.New(script, env.vaultAddr, common.HexToAddress("0xA001"), env.app.Repos.Purchases)

	w := doJSON(t, env.router, http.MethodGet, "/api/v1/sales/inventory", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var inv map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if inv["pendingPurchaseQuote"] != "123456" || inv["quoteBalance"] != "900" || inv["inventory"] != "500" {
		t.Errorf("inventory = %s, want pendingPurchaseQuote 123456 beside balance 900", w.Body.String())
	}
}
