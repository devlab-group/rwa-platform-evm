package bindings

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func v3Config() ProjectConfig {
	return ProjectConfig{
		Name: "Gold Token", Symbol: "GLD", Decimals: 18,
		ProfileDigest: [32]byte{1}, ProjectID: [32]byte{2},
		QuoteToken:                   common.HexToAddress("0x0000000000000000000000000000000000000F"),
		PurchasePricePerWholeToken:   big.NewInt(2000000),
		RedemptionPricePerWholeToken: big.NewInt(1950000),
		MinPurchaseQuote:             big.NewInt(5000000),
		StrategyKind:                 StrategyKindOracle,
		PriceFeed:                    common.HexToAddress("0x0000000000000000000000000000000000FEED"),
		PriceMaxAge:                  3600,
		PurchaseSpreadBps:            25,
		RedemptionSpreadBps:          40,
		SequencerUptimeFeed:          common.HexToAddress("0x0000000000000000000000000000000000005E0"),
		SequencerGracePeriod:         900,
		RedemptionTimeout:            1209600,
		Admin:                        common.HexToAddress("0x0000000000000000000000000000000000AA01"),
		Auditor:                      common.HexToAddress("0x0000000000000000000000000000000000AA02"),
		ComplianceOperator:           common.HexToAddress("0x0000000000000000000000000000000000AA03"),
		Pricer:                       common.HexToAddress("0x0000000000000000000000000000000000AA04"),
		Treasurer:                    common.HexToAddress("0x0000000000000000000000000000000000AA05"),
		RedemptionManager:            common.HexToAddress("0x0000000000000000000000000000000000AA06"),
		IncomeManager:                common.HexToAddress("0x0000000000000000000000000000000000AA08"),
		Treasury:                     common.HexToAddress("0x0000000000000000000000000000000000AA07"),
		AdminTransferDelay:           big.NewInt(0),
	}
}

func TestUnpackDeployInputSelectsLayoutBySelector(t *testing.T) {
	f := NewFactory()
	cfg := v3Config()

	v3Data, err := f.PackDeploy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	v2Data, err := f.PackDeployV2(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(v3Data[:4]) == string(v2Data[:4]) {
		t.Fatal("rwa-v2 and rwa-v3 deploy share a selector")
	}

	got3, err := f.UnpackDeployInput(v3Data)
	if err != nil {
		t.Fatal(err)
	}
	if got3.Version != FactoryVersionV3 {
		t.Errorf("v3 calldata version = %q", got3.Version)
	}
	if got3.StrategyKind != StrategyKindOracle || got3.PriceFeed != cfg.PriceFeed || got3.PriceMaxAge != 3600 ||
		got3.PurchaseSpreadBps != 25 || got3.RedemptionSpreadBps != 40 || got3.SequencerUptimeFeed != cfg.SequencerUptimeFeed ||
		got3.SequencerGracePeriod != 900 || got3.IncomeManager != cfg.IncomeManager || got3.MinPurchaseQuote.Cmp(big.NewInt(5000000)) != 0 {
		t.Errorf("v3 fields lost: %+v", got3)
	}
	if got3.Admin != cfg.Admin || got3.Treasury != cfg.Treasury || got3.RedemptionTimeout != cfg.RedemptionTimeout {
		t.Errorf("shared fields mismatch: %+v", got3)
	}

	got2, err := f.UnpackDeployInput(v2Data)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Version != FactoryVersionV2 {
		t.Errorf("v2 calldata version = %q", got2.Version)
	}
	if got2.Admin != cfg.Admin || got2.Treasury != cfg.Treasury || got2.PurchasePricePerWholeToken.Cmp(cfg.PurchasePricePerWholeToken) != 0 {
		t.Errorf("v2 fields mismatch: %+v", got2)
	}
	if got2.StrategyKind != StrategyKindFixed || got2.IncomeManager != (common.Address{}) || got2.MinPurchaseQuote != nil {
		t.Errorf("v2 call carries v3 fields: %+v", got2)
	}
	// Every shared field survives the explicit v2 copies in PackDeployV2 and UnpackDeployInput;
	// %+v prints big.Int values, so equal amounts compare equal
	want2 := cfg
	want2.MinPurchaseQuote, want2.StrategyKind, want2.PriceFeed, want2.PriceMaxAge = nil, StrategyKindFixed, common.Address{}, 0
	want2.PurchaseSpreadBps, want2.RedemptionSpreadBps, want2.SequencerUptimeFeed, want2.SequencerGracePeriod = 0, 0, common.Address{}, 0
	want2.IncomeManager = common.Address{}
	if got, want := fmt.Sprintf("%+v", got2.ProjectConfig), fmt.Sprintf("%+v", want2); got != want {
		t.Errorf("v2 round trip:\n got %s\nwant %s", got, want)
	}

	if _, err := f.UnpackDeployInput([]byte{1, 2, 3, 4, 5}); err == nil {
		t.Error("unknown selector accepted")
	}
	if _, err := f.UnpackDeployInput([]byte{1}); err == nil {
		t.Error("short calldata accepted")
	}
}

func TestProjectDeployedDecodesBothLayouts(t *testing.T) {
	f := NewFactory()
	addr := func(n byte) common.Address { return common.BytesToAddress([]byte{0xB0, n}) }
	projectID, profileDigest := common.HexToHash("0x03"), common.HexToHash("0x04")

	ev3 := f.ABI.Events["ProjectDeployed"]
	packed3, err := ev3.Inputs.NonIndexed().Pack(addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), addr(7), FactoryVersionV3)
	if err != nil {
		t.Fatal(err)
	}
	got3, err := f.UnpackProjectDeployed(packed3, []common.Hash{ev3.ID, projectID, profileDigest})
	if err != nil {
		t.Fatal(err)
	}
	if got3.IncomeDistributor != addr(7) || got3.Strategy != addr(6) || got3.Layout != FactoryVersionV3 || got3.Version != FactoryVersionV3 {
		t.Errorf("v3 decode: %+v", got3)
	}
	if got3.ProjectID != projectID || got3.ProfileDigest != profileDigest {
		t.Errorf("v3 indexed fields: %+v", got3)
	}

	ev2 := f.ABIV2.Events["ProjectDeployed"]
	if ev2.ID == ev3.ID {
		t.Fatal("rwa-v2 and rwa-v3 ProjectDeployed share a topic0")
	}
	packed2, err := ev2.Inputs.NonIndexed().Pack(addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), FactoryVersionV2)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := f.UnpackProjectDeployed(packed2, []common.Hash{ev2.ID, projectID, profileDigest})
	if err != nil {
		t.Fatal(err)
	}
	if got2.IncomeDistributor != (common.Address{}) || got2.Strategy != addr(6) || got2.Layout != FactoryVersionV2 || got2.Version != FactoryVersionV2 {
		t.Errorf("v2 decode: %+v", got2)
	}

	if !f.IsProjectDeployed(ev2.ID) || !f.IsProjectDeployed(ev3.ID) || f.IsProjectDeployed(common.HexToHash("0x99")) {
		t.Error("IsProjectDeployed must accept exactly the two layouts")
	}
	if _, err := f.UnpackProjectDeployed(packed3, []common.Hash{common.HexToHash("0x99")}); err == nil {
		t.Error("foreign topic0 accepted")
	}
}

func TestVaultMinPurchaseQuote(t *testing.T) {
	v := NewVault()
	// verifyV3Config reads minPurchaseQuote() through this ABI entry
	if id := v.ABI.Methods["minPurchaseQuote"].ID; string(id) != string(selector("minPurchaseQuote()")) {
		t.Fatalf("selector: %x", id)
	}

	event := v.ABI.Events["MinPurchaseQuoteChanged"]
	if event.ID != crypto.Keccak256Hash([]byte("MinPurchaseQuoteChanged(uint256,uint256,address)")) {
		t.Error("topic0 mismatch")
	}
	caller := common.HexToAddress("0x00000000000000000000000000000000000000C1")
	packed, err := event.Inputs.NonIndexed().Pack(big.NewInt(1), big.NewInt(2))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := v.UnpackMinPurchaseQuoteChanged(packed, []common.Hash{event.ID, common.BytesToHash(caller.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	if ev.PreviousMinimum.Int64() != 1 || ev.NewMinimum.Int64() != 2 || ev.Caller != caller {
		t.Errorf("decode: %+v", ev)
	}
}

func TestRWATokenTransferDecode(t *testing.T) {
	tok := NewERC20()
	event := tok.ABI.Events["Transfer"]
	if event.ID != crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)")) {
		t.Fatal("topic0 mismatch")
	}
	from := common.HexToAddress("0x00000000000000000000000000000000000000A1")
	to := common.HexToAddress("0x00000000000000000000000000000000000000B2")
	amount, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	packed, err := event.Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatal(err)
	}
	topics := []common.Hash{event.ID, common.BytesToHash(from.Bytes()), common.BytesToHash(to.Bytes())}
	ev, err := tok.UnpackTransfer(packed, topics)
	if err != nil {
		t.Fatal(err)
	}
	if ev.From != from || ev.To != to || ev.Value.Cmp(amount) != 0 {
		t.Errorf("decode: %+v", ev)
	}
	// An ERC-721 style Transfer has a fourth topic and must not decode as an ERC-20 transfer
	if _, err := tok.UnpackTransfer(packed, append(topics, common.HexToHash("0x01"))); err == nil {
		t.Error("four-topic Transfer accepted")
	}
	if _, err := tok.UnpackTransfer(packed, topics[:2]); err == nil {
		t.Error("two-topic Transfer accepted")
	}
}

func TestIncomeDistributorEvents(t *testing.T) {
	d := NewIncomeDistributor()
	addr := func(n byte) common.Address { return common.BytesToAddress([]byte{0xC0, n}) }
	id := big.NewInt(7)
	idTopic := common.BigToHash(id)
	root := common.HexToHash("0xabcd")
	shaHash := common.HexToHash("0x1234")

	created := d.ABI.Events["DistributionCreated"]
	packed, err := created.Inputs.NonIndexed().Pack(root, big.NewInt(1000), uint64(55), uint64(2000), "ipfs://cid", shaHash)
	if err != nil {
		t.Fatal(err)
	}
	c, err := d.UnpackDistributionCreated(packed, []common.Hash{created.ID, idTopic, common.BytesToHash(addr(1).Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID.Cmp(id) != 0 || c.MerkleRoot != root || c.Total.Int64() != 1000 || c.RecordBlock != 55 || c.ClaimDeadline != 2000 ||
		c.SnapshotURI != "ipfs://cid" || c.SnapshotSha256 != shaHash || c.Caller != addr(1) {
		t.Errorf("DistributionCreated: %+v", c)
	}

	claimed := d.ABI.Events["IncomeClaimed"]
	packed, err = claimed.Inputs.NonIndexed().Pack(big.NewInt(300))
	if err != nil {
		t.Fatal(err)
	}
	cl, err := d.UnpackIncomeClaimed(packed, []common.Hash{claimed.ID, idTopic, common.BytesToHash(addr(2).Bytes())})
	if err != nil || cl.ID.Cmp(id) != 0 || cl.Account != addr(2) || cl.Amount.Int64() != 300 {
		t.Errorf("IncomeClaimed: %+v err=%v", cl, err)
	}

	reclaimed := d.ABI.Events["UnclaimedReclaimed"]
	packed, err = reclaimed.Inputs.NonIndexed().Pack(big.NewInt(9))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := d.UnpackUnclaimedReclaimed(packed, []common.Hash{reclaimed.ID, idTopic, common.BytesToHash(addr(3).Bytes())})
	if err != nil || rc.Treasury != addr(3) || rc.Amount.Int64() != 9 {
		t.Errorf("UnclaimedReclaimed: %+v err=%v", rc, err)
	}

	extended := d.ABI.Events["ClaimDeadlineExtended"]
	packed, err = extended.Inputs.NonIndexed().Pack(uint64(2000), uint64(3000))
	if err != nil {
		t.Fatal(err)
	}
	ex, err := d.UnpackClaimDeadlineExtended(packed, []common.Hash{extended.ID, idTopic, common.BytesToHash(addr(4).Bytes())})
	if err != nil || ex.PreviousDeadline != 2000 || ex.NewDeadline != 3000 || ex.Caller != addr(4) {
		t.Errorf("ClaimDeadlineExtended: %+v err=%v", ex, err)
	}

	for name, fn := range map[string]func() error{
		"created":  func() error { _, err := d.UnpackDistributionCreated(packed, []common.Hash{created.ID}); return err },
		"claimed":  func() error { _, err := d.UnpackIncomeClaimed(packed, []common.Hash{claimed.ID}); return err },
		"reclaim":  func() error { _, err := d.UnpackUnclaimedReclaimed(packed, []common.Hash{reclaimed.ID}); return err },
		"extended": func() error { _, err := d.UnpackClaimDeadlineExtended(packed, []common.Hash{extended.ID}); return err },
	} {
		if fn() == nil {
			t.Errorf("%s: short topic list accepted", name)
		}
	}
}

type dataError struct{ data any }

func (e dataError) Error() string          { return "execution reverted" }
func (e dataError) ErrorData() interface{} { return e.data }

func TestOraclePriceErrorName(t *testing.T) {
	o := NewOraclePriceStrategy()
	sel := func(sig string) []byte { return selector(sig) }
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"stale", append(sel("StalePrice(uint256,uint256)"), make([]byte, 64)...), PriceErrStalePrice},
		{"invalid", append(sel("InvalidPrice(int256,uint256)"), make([]byte, 64)...), PriceErrInvalidPrice},
		{"sequencer down", sel("SequencerDown()"), PriceErrSequencerDown},
		{"grace", append(sel("GracePeriodNotOver(uint256,uint256)"), make([]byte, 64)...), PriceErrGracePeriodNotOver},
		{"decimals", append(sel("FeedDecimalsChanged(uint8,uint8)"), make([]byte, 64)...), PriceErrFeedDecimalsChanged},
		{"constructor error is not a read error", sel("ZeroFeed()"), PriceErrReadFailed},
		{"unknown selector", []byte{1, 2, 3, 4}, PriceErrReadFailed},
		{"empty", nil, PriceErrReadFailed},
		{"short", []byte{1, 2}, PriceErrReadFailed},
	}
	for _, c := range cases {
		if got := o.PriceErrorName(c.data); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRevertData(t *testing.T) {
	if got := RevertData(dataError{data: "0x01020304"}); string(got) != "\x01\x02\x03\x04" {
		t.Errorf("hex payload: %x", got)
	}
	if got := RevertData(dataError{data: 5}); got != nil {
		t.Errorf("non-string payload: %x", got)
	}
	if got := RevertData(dataError{data: "0xzz"}); got != nil {
		t.Errorf("bad hex: %x", got)
	}
	if got := RevertData(errors.New("connection refused")); got != nil {
		t.Errorf("plain error: %x", got)
	}
	if got := RevertData(nil); got != nil {
		t.Errorf("nil error: %x", got)
	}
}

func TestOracleLatestPriceAndRoundData(t *testing.T) {
	o := NewOraclePriceStrategy()
	out, err := o.ABI.Methods["latestPrice"].Outputs.Pack(big.NewInt(123), big.NewInt(456))
	if err != nil {
		t.Fatal(err)
	}
	lp, err := o.UnpackLatestPrice(out)
	if err != nil || lp.Price.Int64() != 123 || lp.UpdatedAt.Int64() != 456 {
		t.Fatalf("latestPrice: %+v err=%v", lp, err)
	}

	agg := NewAggregatorV3()
	round, err := agg.ABI.Methods["latestRoundData"].Outputs.Pack(big.NewInt(1), big.NewInt(2), big.NewInt(3), big.NewInt(789), big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := agg.UnpackUpdatedAt(round); err != nil || got.Int64() != 789 {
		t.Fatalf("updatedAt: %v err=%v", got, err)
	}
}

func TestIncomeManagerRoleName(t *testing.T) {
	if got, ok := RoleName(IncomeManagerRole); !ok || got != "INCOME_MANAGER_ROLE" {
		t.Errorf("RoleName = %q, %v", got, ok)
	}
}
