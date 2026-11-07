package project

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

func TestVerifyDeploymentV4FixedActivates(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v4: true})
	p := verifyV3(t, f)
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s)", p.Status, p.VerificationNote)
	}
	if p.Version != "rwa-v4" || p.PurchaseTimeout != 604800 {
		t.Errorf("version=%q purchaseTimeout=%d, want rwa-v4 and the signed 604800", p.Version, p.PurchaseTimeout)
	}
	// everything rwa-v3 records is still recorded
	if p.Addresses.IncomeDistributor != f.incomeAddr.Hex() || p.MinPurchaseQuote != "5000000" || p.IncomeManager != f.cfg.IncomeManager.Hex() {
		t.Errorf("v3 fields lost: income=%q min=%q manager=%q", p.Addresses.IncomeDistributor, p.MinPurchaseQuote, p.IncomeManager)
	}
	if p.StrategyKind != models.StrategyKindFixed || p.Oracle != nil || p.PurchasePricePerWholeToken != "2000000" {
		t.Errorf("a Fixed v4 deployment keeps its prices: kind=%q oracle=%+v price=%q", p.StrategyKind, p.Oracle, p.PurchasePricePerWholeToken)
	}
}

func TestVerifyDeploymentV4OracleStoresTheDealingCutoff(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v4: true, oracle: true})
	p := verifyV3(t, f)
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s)", p.Status, p.VerificationNote)
	}
	if p.StrategyKind != models.StrategyKindOracle || p.Oracle == nil || p.Oracle.DealingCutoff != 7200 {
		t.Fatalf("oracle = %+v, want the signed 7200 second cut-off", p.Oracle)
	}
	if p.Oracle.MaxAge != 3600 || p.PurchaseTimeout != 604800 {
		t.Errorf("maxAge=%d purchaseTimeout=%d", p.Oracle.MaxAge, p.PurchaseTimeout)
	}
}

func TestVerifyDeploymentV4Failures(t *testing.T) {
	cases := []struct {
		name   string
		opts   fixtureOpts
		mutate func(t *testing.T, f *verifyFixture)
		want   string
	}{
		{
			"purchase timeout differs from the calldata",
			fixtureOpts{v4: true},
			func(t *testing.T, f *verifyFixture) {
				stubFor(t, f.client, f.vaultAddr, bindings.NewVault().ABI, "purchaseTimeout", uint64(86400))
			},
			"vault.purchaseTimeout = 86400, want 604800",
		},
		{
			"oracle dealing cut-off differs from the calldata",
			fixtureOpts{v4: true, oracle: true},
			func(t *testing.T, f *verifyFixture) {
				stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "dealingCutoff", uint64(60))
			},
			"strategy.dealingCutoff = 60, want 7200",
		},
		{
			"oracle strategy that prices instantly",
			fixtureOpts{v4: true, oracle: true},
			func(t *testing.T, f *verifyFixture) {
				stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "forwardPriced", false)
			},
			"strategy.forwardPriced = false, want true",
		},
		{
			"fixed strategy that claims forward pricing",
			fixtureOpts{v4: true},
			func(t *testing.T, f *verifyFixture) {
				stubFor(t, f.client, f.strategyAddr, bindings.NewFixedPriceStrategy().ABI, "forwardPriced", true)
			},
			"strategy.forwardPriced = true, want false",
		},
		{
			"oracle feed differs",
			fixtureOpts{v4: true, oracle: true},
			func(t *testing.T, f *verifyFixture) {
				stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "feed", common.HexToAddress("0xBEEF"))
			},
			"strategy.feed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixtureOpts(t, tc.opts)
			tc.mutate(t, f)
			requireFailed(t, verifyV3(t, f), tc.want)
		})
	}
}

// A Vault that does not answer purchaseTimeout() must not activate, whether the fake node
// errors or answers with an empty word
func TestVerifyDeploymentV4DoesNotActivateWithoutAPurchaseTimeoutAnswer(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v4: true})
	data, err := bindings.NewVault().ABI.Pack("purchaseTimeout")
	if err != nil {
		t.Fatal(err)
	}
	delete(f.client.CallResponses, common.Bytes2Hex(data))
	p, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err == nil && p.Status == models.ProjectStatusActive {
		t.Fatal("a Vault that does not answer purchaseTimeout() was activated")
	}
}

func TestVerifyDeploymentV3DoesNotReadTheV4Views(t *testing.T) {
	for _, opts := range []fixtureOpts{{v3: true}, {v3: true, oracle: true}} {
		f := newVerifyFixtureOpts(t, opts)
		p := verifyV3(t, f)
		if p.Status != models.ProjectStatusActive {
			t.Fatalf("%+v: Status = %s (%s)", opts, p.Status, p.VerificationNote)
		}
		if p.PurchaseTimeout != 0 || (p.Oracle != nil && p.Oracle.DealingCutoff != 0) {
			t.Errorf("%+v: a v3 project carries v4 fields: timeout=%d oracle=%+v", opts, p.PurchaseTimeout, p.Oracle)
		}
		forbidden := map[string]bool{}
		for _, sig := range []string{"purchaseTimeout()", "dealingCutoff()", "forwardPriced()"} {
			forbidden[common.Bytes2Hex(selectorOf(sig))] = true
		}
		for _, call := range f.client.Calls {
			if forbidden[common.Bytes2Hex(call.Data[:4])] {
				t.Errorf("%+v: a v3 verification called %x", opts, call.Data[:4])
			}
		}
	}
}

func TestVerifyDeploymentRejectsV4GenerationDisagreement(t *testing.T) {
	t.Run("v4 calldata with a v3 event version", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v4: true})
		receipt, _ := f.client.TransactionReceipt(context.Background(), f.txHash)
		ev := bindings.NewFactory().ABI.Events["ProjectDeployed"]
		packed, err := ev.Inputs.NonIndexed().Pack(f.tokenAddr, f.complianceAddr, f.controllerAddr, f.vaultAddr, f.escrowAddr, f.strategyAddr, f.incomeAddr, "rwa-v3")
		if err != nil {
			t.Fatal(err)
		}
		receipt.Logs[0].Data = packed
		requireFailed(t, verifyV3(t, f), "deploy calldata is rwa-v4")
	})
	t.Run("v3 calldata with a v4 event version", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v4: true})
		calldata, err := bindings.NewFactory().PackDeployV3(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		f.client.SetTx(f.txHash, signDeployTx(t, adminKey, f.factoryAddr, calldata, 0))
		requireFailed(t, verifyV3(t, f), "deploy calldata is rwa-v3")
	})
	t.Run("v4 calldata with a v2 event", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v4: true})
		receipt, _ := f.client.TransactionReceipt(context.Background(), f.txHash)
		ev2 := bindings.NewFactory().ABIV2.Events["ProjectDeployed"]
		packed, err := ev2.Inputs.NonIndexed().Pack(f.tokenAddr, f.complianceAddr, f.controllerAddr, f.vaultAddr, f.escrowAddr, f.strategyAddr, "rwa-v2")
		if err != nil {
			t.Fatal(err)
		}
		receipt.Logs[0].Topics[0], receipt.Logs[0].Data = ev2.ID, packed
		requireFailed(t, verifyV3(t, f), "deploy calldata is rwa-v4")
	})
	t.Run("live factory version disagrees", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v4: true})
		stubFor(t, f.client, f.factoryAddr, bindings.NewFactory().ABI, "version", "rwa-v3")
		requireFailed(t, verifyV3(t, f), "factory.version()")
	})
}

func TestReconcileDeploymentAdoptsAV4Deployment(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v4: true, oracle: true})
	events := memory.NewChainEventRepository()
	f.seedDeployEvent(t, events)
	if err := f.svc.ReconcileDeployment(context.Background(), events, f.profileRepo); err != nil {
		t.Fatal(err)
	}
	p, err := f.projectRepo.Get(context.Background())
	if err != nil || p.Status != models.ProjectStatusActive || p.Version != "rwa-v4" || p.PurchaseTimeout != 604800 {
		t.Fatalf("project = %+v err=%v", p, err)
	}
}

func TestSupportedFactoryVersionsAreExactlyV2V3V4(t *testing.T) {
	if len(SupportedFactoryVersions) != 3 || !SupportedFactoryVersions["rwa-v2"] || !SupportedFactoryVersions["rwa-v3"] || !SupportedFactoryVersions["rwa-v4"] {
		t.Errorf("SupportedFactoryVersions = %v", SupportedFactoryVersions)
	}
}

func driftV4Project() *models.Project {
	p := driftV3Project()
	p.Version = "rwa-v4"
	p.PurchaseTimeout = 604800
	return p
}

// stubV4Wiring adds the rwa-v4 views CheckConfigDrift reads to stubV3Wiring's baseline
func stubV4Wiring(t *testing.T, c *perContractClient, p *models.Project) {
	t.Helper()
	stubV3Wiring(t, c, p)
	stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "purchaseTimeout", uint64(p.PurchaseTimeout))
	stubView(t, c, common.HexToAddress(p.Addresses.Strategy), bindings.NewFixedPriceStrategy().ABI, "forwardPriced", false)
}

func TestCheckConfigDriftV4(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, c *perContractClient, p *models.Project)
		drift  bool
		wantIn string
	}{
		{name: "clean"},
		{
			name:   "a purchase timeout that changed is drift",
			drift:  true,
			wantIn: "vault.purchaseTimeout",
			mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "purchaseTimeout", uint64(1))
			},
		},
		{
			name:   "a deployed fixed strategy that now claims forward pricing is drift",
			drift:  true,
			wantIn: "strategy.forwardPriced",
			mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				stubView(t, c, common.HexToAddress(p.Addresses.Strategy), bindings.NewFixedPriceStrategy().ABI, "forwardPriced", true)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := driftV4Project()
			c := newPerContractClient()
			stubV4Wiring(t, c, p)
			if tc.mutate != nil {
				tc.mutate(t, c, p)
			}
			ok, reason, err := CheckConfigDrift(context.Background(), c, driftRepo(t, p))
			if err != nil {
				t.Fatal(err)
			}
			if ok == tc.drift {
				t.Fatalf("ok=%v reason=%q, want drift=%v", ok, reason, tc.drift)
			}
			if tc.wantIn != "" && !contains(reason, tc.wantIn) {
				t.Errorf("reason = %q, want %q", reason, tc.wantIn)
			}
		})
	}
}

func TestSecurityFoldsMinPurchaseQuoteForV4(t *testing.T) {
	repos := setupV3(t, func(p *models.Project) { p.Version = "rwa-v4" })
	if got := reconcile(t, repos).MinPurchaseQuote; got != "100" {
		t.Fatalf("baseline = %q, want the deploy-time 100", got)
	}
	addEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 5, 0, map[string]any{"previousMinimum": "100", "newMinimum": "200"})
	if got := reconcile(t, repos).MinPurchaseQuote; got != "200" {
		t.Errorf("rwa-v4 minimum = %q, want 200", got)
	}
}

func TestApplyConfigForV4OracleKeepsEveryField(t *testing.T) {
	cfg := bindings.DeployCall{
		Version: bindings.FactoryVersionV4,
		ProjectConfig: bindings.ProjectConfig{
			StrategyKind:         bindings.StrategyKindOracle,
			PriceFeed:            common.HexToAddress("0xFEED"),
			PriceMaxAge:          3600,
			PurchaseSpreadBps:    25,
			RedemptionSpreadBps:  40,
			SequencerGracePeriod: 900,
			DealingCutoff:        7200,
			RedemptionTimeout:    1209600,
			PurchaseTimeout:      604800,
			MinPurchaseQuote:     big.NewInt(7),
		},
	}
	var p models.Project
	applyConfig(&p, cfg)
	if p.PurchaseTimeout != 604800 || p.RedemptionTimeout != 1209600 || p.MinPurchaseQuote != "7" {
		t.Errorf("timeouts/minimum: %+v", p)
	}
	if p.Oracle == nil || p.Oracle.DealingCutoff != 7200 || p.Oracle.GracePeriod != 900 || p.Oracle.MaxAge != 3600 {
		t.Errorf("oracle = %+v", p.Oracle)
	}
}

func selectorOf(sig string) []byte { return crypto.Keccak256([]byte(sig))[:4] }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
