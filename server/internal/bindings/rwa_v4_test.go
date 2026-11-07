package bindings

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func v4Config() ProjectConfig {
	cfg := v3Config()
	cfg.DealingCutoff = 7200
	cfg.PurchaseTimeout = 604800
	return cfg
}

func TestUnpackDeployInputDecodesRwaV4(t *testing.T) {
	f := NewFactory()
	cfg := v4Config()

	data, err := f.PackDeploy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.UnpackDeployInput(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != FactoryVersionV4 {
		t.Errorf("version = %q, want rwa-v4", got.Version)
	}
	if got.DealingCutoff != 7200 || got.PurchaseTimeout != 604800 {
		t.Errorf("cut-off/purchase timeout = %d/%d, want 7200/604800", got.DealingCutoff, got.PurchaseTimeout)
	}
	// the two new fields sit between sequencerGracePeriod and admin, so a shifted field would
	// move the roles and the redemption timeout with it
	if got.RedemptionTimeout != cfg.RedemptionTimeout || got.SequencerGracePeriod != cfg.SequencerGracePeriod || got.Admin != cfg.Admin {
		t.Errorf("neighbouring fields moved: %+v", got)
	}
	if want, have := fmt.Sprintf("%+v", cfg), fmt.Sprintf("%+v", got.ProjectConfig); want != have {
		t.Errorf("v4 round trip:\n got %s\nwant %s", have, want)
	}
}

func TestDeploySelectorsAreDistinctAndPinned(t *testing.T) {
	f := NewFactory()
	v4, v3, v2 := f.ABI.Methods["deploy"].ID, f.ABIV3.Methods["deploy"].ID, f.ABIV2.Methods["deploy"].ID
	if string(v4) == string(v3) || string(v3) == string(v2) || string(v4) == string(v2) {
		t.Fatalf("deploy selectors collide: v4=%x v3=%x v2=%x", v4, v3, v2)
	}
	// the selectors of the deployed rwa-v3 and the rwa-v4 contracts, from the compiled ABIs
	if fmt.Sprintf("%x", v3) != "c91ac6b4" {
		t.Errorf("rwa-v3 deploy selector = %x, want c91ac6b4", v3)
	}
	if fmt.Sprintf("%x", v4) != "29a5a742" {
		t.Errorf("rwa-v4 deploy selector = %x, want 29a5a742", v4)
	}
}

func TestPackDeployV3DropsTheV4Fields(t *testing.T) {
	f := NewFactory()
	v3Data, err := f.PackDeployV3(v4Config())
	if err != nil {
		t.Fatal(err)
	}
	v4Data, err := f.PackDeploy(v4Config())
	if err != nil {
		t.Fatal(err)
	}
	// two uint64 words fewer
	if len(v4Data)-len(v3Data) != 64 {
		t.Errorf("v4 calldata is %d bytes longer than v3, want 64", len(v4Data)-len(v3Data))
	}
}

func TestProjectDeployedSharesOneTopicAcrossV3AndV4(t *testing.T) {
	f := NewFactory()
	ev := f.ABI.Events["ProjectDeployed"]
	if ev.ID != crypto.Keccak256Hash([]byte("ProjectDeployed(bytes32,bytes32,address,address,address,address,address,address,address,string)")) {
		t.Fatal("ProjectDeployed topic0 changed")
	}
	addr := func(n byte) common.Address { return common.BytesToAddress([]byte{0xB0, n}) }
	packed, err := ev.Inputs.NonIndexed().Pack(addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), addr(7), FactoryVersionV4)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.UnpackProjectDeployed(packed, []common.Hash{ev.ID, common.HexToHash("0x03"), common.HexToHash("0x04")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != FactoryVersionV4 || got.Layout != FactoryVersionV3 || got.IncomeDistributor != addr(7) {
		t.Errorf("v4 event decode: %+v", got)
	}
	if ProjectDeployedLayout(FactoryVersionV4) != FactoryVersionV3 || ProjectDeployedLayout(FactoryVersionV3) != FactoryVersionV3 ||
		ProjectDeployedLayout(FactoryVersionV2) != FactoryVersionV2 {
		t.Error("ProjectDeployedLayout must map v4 and v3 to the v3 layout and keep v2")
	}
	if IsV3OrLater(FactoryVersionV2) || !IsV3OrLater(FactoryVersionV3) || !IsV3OrLater(FactoryVersionV4) || IsV3OrLater("") {
		t.Error("IsV3OrLater must accept rwa-v3 and rwa-v4 only")
	}
}

func TestVaultPurchaseEventsDecode(t *testing.T) {
	v := NewVault()
	addr := func(n byte) common.Address { return common.BytesToAddress([]byte{0xD0, n}) }
	topic := func(a common.Address) common.Hash { return common.BytesToHash(a.Bytes()) }
	id := big.NewInt(12)
	// a feed round id is a uint80, past what uint64 holds
	round := new(big.Int).Lsh(big.NewInt(1), 79)

	req := v.ABI.Events["PurchaseRequested"]
	packed, err := req.Inputs.NonIndexed().Pack(big.NewInt(1000), big.NewInt(2500), addr(3), round, uint64(1700000000))
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.UnpackPurchaseRequested(packed, []common.Hash{req.ID, common.BigToHash(id), topic(addr(1)), topic(addr(2))})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID.Cmp(id) != 0 || r.Buyer != addr(1) || r.Recipient != addr(2) || r.TokenAmount.Int64() != 1000 ||
		r.MaxQuoteAmount.Int64() != 2500 || r.Strategy != addr(3) || r.BaseRound.Cmp(round) != 0 || r.CreatedAt != 1700000000 {
		t.Errorf("PurchaseRequested: %+v", r)
	}

	for _, name := range []string{"PurchaseSettled", "PurchaseRefunded"} {
		ev := v.ABI.Events[name]
		packed, err := ev.Inputs.NonIndexed().Pack(round, big.NewInt(2400), big.NewInt(100))
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.UnpackPurchasePriced(name, packed, []common.Hash{ev.ID, common.BigToHash(id)})
		if err != nil {
			t.Fatal(err)
		}
		if got.ID.Cmp(id) != 0 || got.RoundID.Cmp(round) != 0 || got.QuoteAmount.Int64() != 2400 || got.Refund.Int64() != 100 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	for _, name := range []string{"PurchaseCancelled", "PurchaseRejected"} {
		ev := v.ABI.Events[name]
		packed, err := ev.Inputs.NonIndexed().Pack(big.NewInt(2500))
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.UnpackPurchaseClosed(name, packed, []common.Hash{ev.ID, common.BigToHash(id), topic(addr(1))})
		if err != nil {
			t.Fatal(err)
		}
		if got.ID.Cmp(id) != 0 || got.Buyer != addr(1) || got.Refund.Int64() != 2500 {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestVaultPurchaseOrderViews(t *testing.T) {
	v := NewVault()
	want := PurchaseOrderTuple{
		Buyer:            common.HexToAddress("0x01"),
		CreatedAt:        1700000000,
		Status:           1,
		Recipient:        common.HexToAddress("0x02"),
		Strategy:         common.HexToAddress("0x03"),
		BaseRound:        new(big.Int).Lsh(big.NewInt(1), 79),
		TokenAmount:      big.NewInt(1000),
		MaxQuoteAmount:   big.NewInt(2500),
		MinPurchaseQuote: big.NewInt(900),
	}
	packed, err := v.ABI.Methods["getPurchase"].Outputs.Pack(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.UnpackGetPurchase(packed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Buyer != want.Buyer || got.CreatedAt != want.CreatedAt || got.Status != want.Status || got.Recipient != want.Recipient ||
		got.Strategy != want.Strategy || got.BaseRound.Cmp(want.BaseRound) != 0 || got.TokenAmount.Cmp(want.TokenAmount) != 0 ||
		got.MaxQuoteAmount.Cmp(want.MaxQuoteAmount) != 0 || got.MinPurchaseQuote.Cmp(want.MinPurchaseQuote) != 0 {
		t.Errorf("getPurchase: got %+v want %+v", got, want)
	}

	for name, sig := range map[string]string{
		"requestPurchase":      "requestPurchase(uint256,uint256,address,uint64)",
		"settlePurchase":       "settlePurchase(uint256)",
		"cancelPurchase":       "cancelPurchase(uint256)",
		"rejectPurchase":       "rejectPurchase(uint256)",
		"getPurchase":          "getPurchase(uint256)",
		"nextPurchaseId":       "nextPurchaseId()",
		"pendingPurchaseQuote": "pendingPurchaseQuote()",
		"purchaseTimeout":      "purchaseTimeout()",
	} {
		if string(v.ABI.Methods[name].ID) != string(selector(sig)) {
			t.Errorf("%s selector = %x, want that of %s", name, v.ABI.Methods[name].ID, sig)
		}
	}

	out, err := v.ABI.Methods["pendingPurchaseQuote"].Outputs.Pack(big.NewInt(77))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := v.UnpackPendingPurchaseQuote(out); err != nil || pending.Int64() != 77 {
		t.Errorf("pendingPurchaseQuote = %v, %v", pending, err)
	}
	if next, err := v.UnpackNextPurchaseId(out); err == nil && next.Int64() != 77 {
		t.Errorf("nextPurchaseId = %v", next)
	}
}

func TestEscrowForwardPricingEventsDecode(t *testing.T) {
	r := NewRedemptionEscrow()
	strategy := common.HexToAddress("0x000000000000000000000000000000000000005A")
	id := big.NewInt(4)
	round := new(big.Int).Lsh(big.NewInt(1), 79)

	fp := r.ABI.Events["RedemptionForwardPriced"]
	packed, err := fp.Inputs.NonIndexed().Pack(round, big.NewInt(900))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.UnpackRedemptionForwardPriced(packed, []common.Hash{fp.ID, common.BigToHash(id), common.BytesToHash(strategy.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID.Cmp(id) != 0 || got.Strategy != strategy || got.BaseRound.Cmp(round) != 0 || got.MinQuoteOut.Int64() != 900 {
		t.Errorf("RedemptionForwardPriced: %+v", got)
	}

	pr := r.ABI.Events["RedemptionPriced"]
	packed, err = pr.Inputs.NonIndexed().Pack(round, big.NewInt(950))
	if err != nil {
		t.Fatal(err)
	}
	priced, err := r.UnpackRedemptionPriced(packed, []common.Hash{pr.ID, common.BigToHash(id)})
	if err != nil {
		t.Fatal(err)
	}
	if priced.ID.Cmp(id) != 0 || priced.RoundID.Cmp(round) != 0 || priced.QuoteAmount.Int64() != 950 {
		t.Errorf("RedemptionPriced: %+v", priced)
	}

	out, err := r.ABI.Methods["previewFunding"].Outputs.Pack(big.NewInt(950))
	if err != nil {
		t.Fatal(err)
	}
	if q, err := r.UnpackPreviewFunding(out); err != nil || q.Int64() != 950 {
		t.Errorf("previewFunding = %v, %v", q, err)
	}
}

func TestOracleStrategyForwardPricingViews(t *testing.T) {
	o := NewOraclePriceStrategy()
	for name, sig := range map[string]string{
		"dealingCutoff":        "dealingCutoff()",
		"forwardPriced":        "forwardPriced()",
		"latestRound":          "latestRound()",
		"quotePurchaseAfter":   "quotePurchaseAfter(uint256,uint80,uint64)",
		"quoteRedemptionAfter": "quoteRedemptionAfter(uint256,uint80,uint64)",
	} {
		if string(o.ABI.Methods[name].ID) != string(selector(sig)) {
			t.Errorf("%s selector = %x, want that of %s", name, o.ABI.Methods[name].ID, sig)
		}
	}
	round := new(big.Int).Lsh(big.NewInt(1), 79)
	out, err := o.ABI.Methods["quotePurchaseAfter"].Outputs.Pack(big.NewInt(2500), round)
	if err != nil {
		t.Fatal(err)
	}
	q, err := o.UnpackQuoteAfter("quotePurchaseAfter", out)
	if err != nil || q.QuoteAmount.Int64() != 2500 || q.RoundID.Cmp(round) != 0 {
		t.Errorf("quotePurchaseAfter = %+v, %v", q, err)
	}
	if _, err := o.PackQuoteAfter("quoteRedemptionAfter", big.NewInt(1), round, 1700000000); err != nil {
		t.Errorf("pack quoteRedemptionAfter: %v", err)
	}
	if string(NewFixedPriceStrategy().ABI.Methods["forwardPriced"].ID) != string(selector("forwardPriced()")) {
		t.Error("FixedPriceStrategy must declare forwardPriced()")
	}
}

// the pack helpers feed wallet transactions, so each argument must land in its own slot
func TestPurchaseAndFundingPackHelpersEncodeArgumentsInOrder(t *testing.T) {
	v, e := NewVault(), NewRedemptionEscrow()
	recipient := common.HexToAddress("0x00000000000000000000000000000000000000c2")
	id := big.NewInt(77)

	data, err := v.PackRequestPurchase(big.NewInt(1000), big.NewInt(2500), recipient, 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	args, err := v.ABI.Methods["requestPurchase"].Inputs.Unpack(data[4:])
	if err != nil || string(data[:4]) != string(v.ABI.Methods["requestPurchase"].ID) {
		t.Fatalf("requestPurchase: %v, selector %x", err, data[:4])
	}
	if args[0].(*big.Int).Int64() != 1000 || args[1].(*big.Int).Int64() != 2500 || args[2].(common.Address) != recipient || args[3].(uint64) != 1700000000 {
		t.Errorf("requestPurchase args = %v, want tokenAmount 1000, maxQuoteAmount 2500, recipient, deadline", args)
	}

	for name, pack := range map[string]func(*big.Int) ([]byte, error){
		"settlePurchase": v.PackSettlePurchase,
		"cancelPurchase": v.PackCancelPurchase,
		"rejectPurchase": v.PackRejectPurchase,
		"getPurchase":    v.PackGetPurchase,
	} {
		data, err := pack(id)
		if err != nil {
			t.Fatal(err)
		}
		args, err := v.ABI.Methods[name].Inputs.Unpack(data[4:])
		if err != nil || string(data[:4]) != string(v.ABI.Methods[name].ID) || args[0].(*big.Int).Cmp(id) != 0 {
			t.Errorf("%s: args %v err %v selector %x", name, args, err, data[:4])
		}
	}
	for name, pack := range map[string]func() ([]byte, error){
		"pendingPurchaseQuote": v.PackPendingPurchaseQuote,
		"nextPurchaseId":       v.PackNextPurchaseId,
	} {
		if data, err := pack(); err != nil || string(data) != string(v.ABI.Methods[name].ID) {
			t.Errorf("%s calldata = %x, err %v, want just the selector", name, data, err)
		}
	}
	for name, pack := range map[string]func(*big.Int) ([]byte, error){
		"fundRedemption": e.PackFundRedemption,
		"previewFunding": e.PackPreviewFunding,
	} {
		data, err := pack(id)
		if err != nil {
			t.Fatal(err)
		}
		args, err := e.ABI.Methods[name].Inputs.Unpack(data[4:])
		if err != nil || string(data[:4]) != string(e.ABI.Methods[name].ID) || args[0].(*big.Int).Cmp(id) != 0 {
			t.Errorf("%s: args %v err %v selector %x", name, args, err, data[:4])
		}
	}
}
