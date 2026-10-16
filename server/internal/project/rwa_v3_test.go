package project

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// stubFor overrides one view on one contract, ahead of the fixture's calldata-only stubs
func stubFor(t *testing.T, c *perContractClient, contract common.Address, contractABI abi.ABI, method string, value any) {
	t.Helper()
	data, err := contractABI.Pack(method)
	if err != nil {
		t.Fatal(err)
	}
	out, err := contractABI.Methods[method].Outputs.Pack(value)
	if err != nil {
		t.Fatal(err)
	}
	m := c.byContract[contract]
	if m == nil {
		m = map[string][]byte{}
		c.byContract[contract] = m
	}
	m[common.Bytes2Hex(data)] = out
}

func verifyV3(t *testing.T, f *verifyFixture) *models.Project {
	t.Helper()
	p, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	return p
}

func requireFailed(t *testing.T, p *models.Project, reason string) {
	t.Helper()
	if p.Status != models.ProjectStatusFailed {
		t.Fatalf("Status = %s, want Failed (%q)", p.Status, reason)
	}
	if !strings.Contains(p.VerificationNote, reason) {
		t.Fatalf("VerificationNote = %q, want it to mention %q", p.VerificationNote, reason)
	}
}

func TestVerifyDeploymentV3FixedActivates(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
	p := verifyV3(t, f)
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s)", p.Status, p.VerificationNote)
	}
	if p.Version != "rwa-v3" || p.Addresses.IncomeDistributor != f.incomeAddr.Hex() {
		t.Errorf("version=%q income=%q", p.Version, p.Addresses.IncomeDistributor)
	}
	if p.MinPurchaseQuote != "5000000" || p.IncomeManager != f.cfg.IncomeManager.Hex() {
		t.Errorf("minPurchaseQuote=%q incomeManager=%q", p.MinPurchaseQuote, p.IncomeManager)
	}
	if p.StrategyKind != models.StrategyKindFixed || p.Oracle != nil {
		t.Errorf("strategy kind=%q oracle=%+v", p.StrategyKind, p.Oracle)
	}
	if p.PurchasePricePerWholeToken != "2000000" {
		t.Errorf("a Fixed v3 deployment must keep its prices, got %q", p.PurchasePricePerWholeToken)
	}
	if got := p.Roles["INCOME_MANAGER_ROLE"]; len(got) != 1 || got[0] != f.cfg.IncomeManager.Hex() {
		t.Errorf("INCOME_MANAGER_ROLE holders = %v", got)
	}
}

func TestVerifyDeploymentV3OracleActivatesWithoutStrategyRoles(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v3: true, oracle: true})
	p := verifyV3(t, f)
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s)", p.Status, p.VerificationNote)
	}
	if p.StrategyKind != models.StrategyKindOracle || p.Oracle == nil {
		t.Fatalf("strategy kind=%q oracle=%+v", p.StrategyKind, p.Oracle)
	}
	if p.Oracle.MaxAge != 3600 || p.Oracle.PurchaseSpreadBps != 25 || p.Oracle.RedemptionSpreadBps != 40 || p.Oracle.GracePeriod != 900 ||
		p.Oracle.Feed != f.cfg.PriceFeed.Hex() || p.Oracle.SequencerUptimeFeed != f.cfg.SequencerUptimeFeed.Hex() {
		t.Errorf("oracle config = %+v", p.Oracle)
	}
	if p.PurchasePricePerWholeToken != "" || p.RedemptionPricePerWholeToken != "" {
		t.Errorf("an Oracle deployment stores no prices, got %q / %q", p.PurchasePricePerWholeToken, p.RedemptionPricePerWholeToken)
	}
	// no role call may have gone to the oracle strategy
	for _, call := range f.client.Calls {
		if call.To != nil && *call.To == f.strategyAddr {
			sel := common.Bytes2Hex(call.Data[:4])
			for _, name := range []string{"getRoleMemberCount", "getRoleMember", "hasRole"} {
				if sel == common.Bytes2Hex(bindings.NewAccessControl().ABI.Methods[name].ID) {
					t.Errorf("%s was called on the OraclePriceStrategy", name)
				}
			}
		}
	}
}

func TestVerifyDeploymentV3Failures(t *testing.T) {
	attacker := common.HexToAddress("0x000000000000000000000000000000000000DEAD")
	cases := []struct {
		name   string
		opts   fixtureOpts
		mutate func(t *testing.T, f *verifyFixture)
		want   string
	}{
		{"no code at the income distributor", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			delete(f.client.Code, f.incomeAddr)
		}, "no on-chain code"},
		{"unexpected income manager", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubRoleHolders(t, f.client, f.incomeAddr, bindings.IncomeManagerRole, f.cfg.IncomeManager, attacker)
		}, "INCOME_MANAGER_ROLE on incomeDistributor held by unexpected"},
		{"missing income manager", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubRoleHolders(t, f.client, f.incomeAddr, bindings.IncomeManagerRole)
		}, "missing required roles"},
		{"income manager role on the vault", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubRoleHolders(t, f.client, f.vaultAddr, bindings.IncomeManagerRole, attacker)
		}, "INCOME_MANAGER_ROLE on vault held by unexpected"},
		{"unexpected admin on the income distributor", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubRoleHolders(t, f.client, f.incomeAddr, bindings.DefaultAdminRole, f.cfg.Admin, attacker)
		}, "DEFAULT_ADMIN_ROLE on incomeDistributor held by unexpected"},
		{"income distributor points at another vault", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.incomeAddr, bindings.NewIncomeDistributor().ABI, "vault", attacker)
		}, "incomeDistributor.vault"},
		{"income distributor points at another token", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.incomeAddr, bindings.NewIncomeDistributor().ABI, "token", attacker)
		}, "incomeDistributor.token"},
		{"income distributor pays another quote token", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.incomeAddr, bindings.NewIncomeDistributor().ABI, "quoteToken", attacker)
		}, "incomeDistributor.quoteToken"},
		{"minimum purchase differs from the calldata", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.vaultAddr, bindings.NewVault().ABI, "minPurchaseQuote", big.NewInt(1))
		}, "vault.minPurchaseQuote = 1, want 5000000"},
		{"rwa-v3 escrow follows a different strategy than the vault", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.escrowAddr, bindings.NewRedemptionEscrow().ABI, "strategy", attacker)
		}, "escrow.strategy"},
		{"factory retains the income manager role", fixtureOpts{v3: true}, func(t *testing.T, f *verifyFixture) {
			ac := bindings.NewAccessControl()
			data, _ := ac.PackHasRole(bindings.IncomeManagerRole, f.factoryAddr)
			out, _ := ac.ABI.Methods["hasRole"].Outputs.Pack(true)
			f.client.byContract[f.incomeAddr] = map[string][]byte{common.Bytes2Hex(data): out}
			stubRoleHolders(t, f.client, f.incomeAddr, bindings.IncomeManagerRole, f.cfg.IncomeManager)
		}, "INCOME_MANAGER_ROLE on IncomeDistributor"},
		{"oracle feed differs", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "feed", attacker)
		}, "strategy.feed"},
		{"oracle sequencer feed differs", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "sequencerUptimeFeed", common.Address{})
		}, "strategy.sequencerUptimeFeed"},
		{"oracle max age differs", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "maxAge", uint64(86400))
		}, "strategy.maxAge = 86400, want 3600"},
		{"oracle grace period differs", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "gracePeriod", uint64(1))
		}, "strategy.gracePeriod"},
		{"oracle purchase spread differs", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "purchaseSpreadBps", uint16(26))
		}, "strategy.purchaseSpreadBps = 26, want 25"},
		{"oracle redemption spread differs", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "redemptionSpreadBps", uint16(41))
		}, "strategy.redemptionSpreadBps = 41, want 40"},
		{"oracle quote decimals differ from the quote token", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewOraclePriceStrategy().ABI, "quoteDecimals", uint8(18))
		}, "strategy.quoteDecimals = 18, want 6"},
		{"oracle token decimals differ from the profile", fixtureOpts{v3: true, oracle: true}, func(t *testing.T, f *verifyFixture) {
			stubFor(t, f.client, f.strategyAddr, bindings.NewFixedPriceStrategy().ABI, "tokenDecimals", uint8(8))
		}, "strategy.tokenDecimals = 8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixtureOpts(t, tc.opts)
			tc.mutate(t, f)
			requireFailed(t, verifyV3(t, f), tc.want)
		})
	}
}

// Both factory generations must stay in agreement: the calldata selector, the event layout
// and the event's version string
func TestVerifyDeploymentV3RejectsGenerationDisagreement(t *testing.T) {
	t.Run("v3 calldata with a v2 event", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
		receipt, _ := f.client.TransactionReceipt(context.Background(), f.txHash)
		ev2 := bindings.NewFactory().ABIV2.Events["ProjectDeployed"]
		packed, err := ev2.Inputs.NonIndexed().Pack(f.tokenAddr, f.complianceAddr, f.controllerAddr, f.vaultAddr, f.escrowAddr, f.strategyAddr, "rwa-v2")
		if err != nil {
			t.Fatal(err)
		}
		receipt.Logs[0].Topics[0], receipt.Logs[0].Data = ev2.ID, packed
		requireFailed(t, verifyV3(t, f), "deploy calldata is rwa-v3")
	})
	t.Run("v2 calldata with a v3 event", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
		calldata, err := bindings.NewFactory().PackDeployV2(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		f.client.SetTx(f.txHash, signDeployTx(t, adminKey, f.factoryAddr, calldata, 0))
		requireFailed(t, verifyV3(t, f), "deploy calldata is rwa-v2")
	})
	t.Run("v3 layout reporting the v2 version string", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
		receipt, _ := f.client.TransactionReceipt(context.Background(), f.txHash)
		ev3 := bindings.NewFactory().ABI.Events["ProjectDeployed"]
		packed, err := ev3.Inputs.NonIndexed().Pack(f.tokenAddr, f.complianceAddr, f.controllerAddr, f.vaultAddr, f.escrowAddr, f.strategyAddr, f.incomeAddr, "rwa-v2")
		if err != nil {
			t.Fatal(err)
		}
		receipt.Logs[0].Data = packed
		requireFailed(t, verifyV3(t, f), "reports version")
	})
	t.Run("live factory version disagrees", func(t *testing.T) {
		f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
		stubFor(t, f.client, f.factoryAddr, bindings.NewFactory().ABI, "version", "rwa-v2")
		requireFailed(t, verifyV3(t, f), "factory.version()")
	})
}

// An rwa-v2 deployment is verified exactly as before: no income, minimum or oracle checks,
// and the escrow's immutable strategy is the deployed one
func TestVerifyDeploymentV2IsUnchanged(t *testing.T) {
	f := newVerifyFixture(t)
	p := verifyV3(t, f)
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s)", p.Status, p.VerificationNote)
	}
	if p.Addresses.IncomeDistributor != "" || p.MinPurchaseQuote != "" || p.IncomeManager != "" {
		t.Errorf("v2 project carries v3 fields: %+v", p)
	}
	if p.StrategyKind != models.StrategyKindFixed {
		t.Errorf("StrategyKind = %q", p.StrategyKind)
	}
	for _, call := range f.client.Calls {
		if call.To != nil && *call.To == f.incomeAddr {
			t.Error("a v2 verification called the income distributor")
		}
	}
	// the v2 escrow check still compares with the deployed strategy, not the vault's
	f2 := newVerifyFixture(t)
	stubFor(t, f2.client, f2.escrowAddr, bindings.NewRedemptionEscrow().ABI, "strategy", common.HexToAddress("0xBEEF"))
	requireFailed(t, verifyV3(t, f2), "escrow.strategy")
}

func TestVerifyBytecodeIncludesTheIncomeDistributor(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
	addrs := models.Addresses{
		Token: f.tokenAddr.Hex(), Compliance: f.complianceAddr.Hex(), SupplyController: f.controllerAddr.Hex(),
		Vault: f.vaultAddr.Hex(), RedemptionEscrow: f.escrowAddr.Hex(), Strategy: f.strategyAddr.Hex(), IncomeDistributor: f.incomeAddr.Hex(),
	}
	ok, err := verifyBytecode(context.Background(), f.client, addrs)
	if err != nil || !ok {
		t.Fatalf("all seven present: %v %v", ok, err)
	}
	delete(f.client.Code, f.incomeAddr)
	if ok, _ := verifyBytecode(context.Background(), f.client, addrs); ok {
		t.Error("missing income distributor code went unnoticed")
	}
}

func TestFactoryVersionIsCachedAndEmptyWhenUnreadable(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
	if got := f.svc.FactoryVersion(context.Background()); got != "rwa-v3" {
		t.Fatalf("FactoryVersion = %q", got)
	}
	calls := len(f.client.Calls)
	if got := f.svc.FactoryVersion(context.Background()); got != "rwa-v3" || len(f.client.Calls) != calls {
		t.Errorf("second read = %q after %d extra calls, want a cache hit", got, len(f.client.Calls)-calls)
	}

	// no canned answer at all: the read fails and the version is reported empty
	bare := newPerContractClient()
	bare.DefaultCallResponse = nil
	svc := New(bare, common.HexToAddress("0xFACE"), memory.NewProjectRepository(), testChainID, common.Address{})
	clock := time.Unix(1_700_000_000, 0)
	svc.now = func() time.Time { return clock }
	if got := svc.FactoryVersion(context.Background()); got != "" {
		t.Errorf("unreadable factory reported %q", got)
	}
	// a failed read is remembered for versionRetryAfter, then retried: once the factory
	// answers, the version appears
	failedCalls := len(bare.Calls)
	svc.FactoryVersion(context.Background())
	if len(bare.Calls) != failedCalls {
		t.Errorf("a failed read was retried within the backoff: %d extra calls", len(bare.Calls)-failedCalls)
	}
	clock = clock.Add(versionRetryAfter)
	versionData, _ := bindings.NewFactory().ABI.Pack("version")
	out, _ := bindings.NewFactory().ABI.Methods["version"].Outputs.Pack("rwa-v2")
	bare.CallResponses[common.Bytes2Hex(versionData)] = out
	if got := svc.FactoryVersion(context.Background()); got != "rwa-v2" {
		t.Errorf("after recovery = %q", got)
	}
	// no factory configured
	if got := New(bare, common.Address{}, memory.NewProjectRepository(), testChainID, common.Address{}).FactoryVersion(context.Background()); got != "" {
		t.Errorf("no factory configured reported %q", got)
	}

	// a caller that disconnects from GET /config does not make the read fail for everyone
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	detached := New(ctxClient{bare}, common.HexToAddress("0xFACE"), memory.NewProjectRepository(), testChainID, common.Address{})
	if got := detached.FactoryVersion(cancelled); got != "rwa-v2" {
		t.Errorf("read with a cancelled request context = %q, want rwa-v2", got)
	}
}

// ctxClient fails a call once its context is done, as a real RPC client does
type ctxClient struct{ blockchain.Client }

func (c ctxClient) CallContract(ctx context.Context, msg ethereum.CallMsg, n *big.Int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Client.CallContract(ctx, msg, n)
}

func TestReconcileDeploymentAdoptsAV3Deployment(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v3: true})
	events := memory.NewChainEventRepository()
	f.seedDeployEvent(t, events)
	if err := f.svc.ReconcileDeployment(context.Background(), events, f.profileRepo); err != nil {
		t.Fatal(err)
	}
	p, err := f.projectRepo.Get(context.Background())
	if err != nil || p.Status != models.ProjectStatusActive || p.Version != "rwa-v3" {
		t.Fatalf("project = %+v err=%v", p, err)
	}
}

var (
	incomeAddr = addr("0x0000000000000000000000000000000000000007")
	incomeMgr  = addr("0x0000000000000000000000000000000000000409")
)

// v3Project is activeProject as an rwa-v3 deployment with an income distributor
func v3Project() *models.Project {
	p := activeProject()
	p.Version = "rwa-v3"
	p.Addresses.IncomeDistributor = incomeAddr
	p.IncomeManager = incomeMgr
	p.MinPurchaseQuote = "100"
	p.StrategyKind = models.StrategyKindFixed
	return p
}

func setupV3(t *testing.T, mutate func(p *models.Project)) *repository.Repositories {
	t.Helper()
	repos := setup(t)
	p := v3Project()
	if mutate != nil {
		mutate(p)
	}
	if err := repos.Projects.Upsert(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return repos
}

func TestSecurityFoldsMinPurchaseQuote(t *testing.T) {
	repos := setupV3(t, nil)
	if got := reconcile(t, repos).MinPurchaseQuote; got != "100" {
		t.Fatalf("baseline = %q, want the deploy-time 100", got)
	}
	addEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 5, 0, map[string]any{"previousMinimum": "100", "newMinimum": "200"})
	addEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 7, 2, map[string]any{"previousMinimum": "200", "newMinimum": "300"})
	addEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 7, 1, map[string]any{"previousMinimum": "200", "newMinimum": "250"})
	if got := reconcile(t, repos).MinPurchaseQuote; got != "300" {
		t.Errorf("latest by (block, logIndex) = %q, want 300", got)
	}
	addRemovedEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 9, 0, map[string]any{"previousMinimum": "300", "newMinimum": "999"})
	if got := reconcile(t, repos).MinPurchaseQuote; got != "300" {
		t.Errorf("a reorged event changed the minimum to %q", got)
	}
	// setting the minimum back to zero is an event like any other
	addEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 11, 0, map[string]any{"previousMinimum": "300", "newMinimum": "0"})
	if got := reconcile(t, repos).MinPurchaseQuote; got != "0" {
		t.Errorf("minimum reset to zero = %q", got)
	}
}

func TestSecurityLeavesMinPurchaseQuoteEmptyForV2(t *testing.T) {
	repos := setup(t) // rwa-v2: no Version, no minimum
	addEvent(t, repos, vaultAddr, "MinPurchaseQuoteChanged", 5, 0, map[string]any{"newMinimum": "200"})
	if got := reconcile(t, repos).MinPurchaseQuote; got != "" {
		t.Errorf("a v2 project reported minPurchaseQuote %q", got)
	}
}

func TestSecurityRolesOnTheIncomeDistributor(t *testing.T) {
	repos := setupV3(t, nil)
	s := reconcile(t, repos)
	by := s.RolesByContract["incomeDistributor"]
	if len(by["INCOME_MANAGER_ROLE"]) != 1 || by["INCOME_MANAGER_ROLE"][0] != incomeMgr || len(by["DEFAULT_ADMIN_ROLE"]) != 1 || by["DEFAULT_ADMIN_ROLE"][0] != adminA {
		t.Fatalf("seeded roles = %v", by)
	}
	if got := s.Roles["INCOME_MANAGER_ROLE"]; len(got) != 1 || got[0] != incomeMgr {
		t.Errorf("Roles = %v", got)
	}

	// the manager rotates: grant the new one, revoke the old one
	newMgr := addr("0x0000000000000000000000000000000000000510")
	addEvent(t, repos, incomeAddr, "RoleGranted", 5, 0, map[string]any{"role": roleHex(bindings.IncomeManagerRole), "account": newMgr, "sender": adminA})
	addEvent(t, repos, incomeAddr, "RoleRevoked", 6, 0, map[string]any{"role": roleHex(bindings.IncomeManagerRole), "account": incomeMgr, "sender": adminA})
	by = reconcile(t, repos).RolesByContract["incomeDistributor"]
	if len(by["INCOME_MANAGER_ROLE"]) != 1 || by["INCOME_MANAGER_ROLE"][0] != newMgr {
		t.Errorf("after rotation = %v", by["INCOME_MANAGER_ROLE"])
	}

	// an admin transfer scheduled on the income distributor alone is still pending
	addEvent(t, repos, incomeAddr, "DefaultAdminTransferScheduled", 8, 0, map[string]any{"newAdmin": newAdminB})
	if got := reconcile(t, repos).PendingAdmin; got != newAdminB {
		t.Errorf("PendingAdmin = %q, want %q", got, newAdminB)
	}
	// and the new admin holding DEFAULT_ADMIN there counts as accepted
	addEvent(t, repos, incomeAddr, "RoleGranted", 9, 0, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": newAdminB, "sender": adminA})
	addEvent(t, repos, incomeAddr, "RoleRevoked", 9, 1, map[string]any{"role": roleHex(bindings.DefaultAdminRole), "account": adminA, "sender": adminA})
	s = reconcile(t, repos)
	if s.PendingAdmin != "" {
		t.Errorf("PendingAdmin = %q after acceptance", s.PendingAdmin)
	}
	if got := s.RolesByContract["incomeDistributor"]["DEFAULT_ADMIN_ROLE"]; len(got) != 1 || got[0] != newAdminB {
		t.Errorf("income distributor admin = %v, want [%s]", got, newAdminB)
	}
}

func TestSecuritySeedsNoRolesOnADeployedOracleStrategy(t *testing.T) {
	repos := setupV3(t, func(p *models.Project) {
		p.StrategyKind = models.StrategyKindOracle
		p.PurchasePricePerWholeToken, p.RedemptionPricePerWholeToken = "", ""
	})
	s := reconcile(t, repos)
	if _, has := s.RolesByContract["strategy"]; has {
		t.Errorf("an OraclePriceStrategy has no roles, got %v", s.RolesByContract["strategy"])
	}
	if s.Pricer != "" {
		t.Errorf("Pricer = %q for an oracle strategy", s.Pricer)
	}
	if s.PurchasePricePerWholeToken != "" {
		t.Errorf("prices must come from the strategy at request time, got %q", s.PurchasePricePerWholeToken)
	}
	if got := s.Strategy; got != strategyAddr {
		t.Errorf("Strategy = %q", got)
	}
}

// stubV3Wiring adds the rwa-v3 views CheckConfigDrift reads to stubWiring's baseline
func stubV3Wiring(t *testing.T, c *perContractClient, p *models.Project) {
	t.Helper()
	stubWiring(t, c, p)
	a := p.Addresses
	income := common.HexToAddress(a.IncomeDistributor)
	incomeABI := bindings.NewIncomeDistributor().ABI
	stubView(t, c, income, incomeABI, "token", common.HexToAddress(a.Token))
	stubView(t, c, income, incomeABI, "quoteToken", common.HexToAddress(p.QuoteToken))
	stubView(t, c, income, incomeABI, "vault", common.HexToAddress(a.Vault))
	min, _ := new(big.Int).SetString(p.MinPurchaseQuote, 10)
	stubView(t, c, common.HexToAddress(a.Vault), bindings.NewVault().ABI, "minPurchaseQuote", min)
}

func driftV3Project() *models.Project {
	p := driftProject()
	p.Version = "rwa-v3"
	p.Addresses.IncomeDistributor = "0x0000000000000000000000000000000000B007"
	p.MinPurchaseQuote = "5000000"
	p.StrategyKind = models.StrategyKindFixed
	return p
}

func TestCheckConfigDriftV3(t *testing.T) {
	newStrategy := common.HexToAddress("0x0000000000000000000000000000000000B0AA")
	cases := []struct {
		name    string
		mutate  func(t *testing.T, c *perContractClient, p *models.Project)
		drift   bool
		wantIn  string
		wantOut string
	}{
		{name: "clean"},
		{
			name: "a vault strategy swap the escrow follows is not drift", mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				p.Security = &models.SecurityState{Strategy: newStrategy.Hex()}
				stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "strategy", newStrategy)
				stubView(t, c, common.HexToAddress(p.Addresses.RedemptionEscrow), bindings.NewRedemptionEscrow().ABI, "strategy", newStrategy)
			},
		},
		{
			name: "an escrow that no longer follows the vault is drift", drift: true, wantIn: "escrow.strategy",
			mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				p.Security = &models.SecurityState{Strategy: newStrategy.Hex()}
				stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "strategy", newStrategy)
			},
		},
		{
			name: "a projected minimum purchase change is accepted", mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				p.Security = &models.SecurityState{MinPurchaseQuote: "7"}
				stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "minPurchaseQuote", big.NewInt(7))
			},
		},
		{
			name: "a minimum purchase changed without an event is drift", drift: true, wantIn: "vault.minPurchaseQuote",
			mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "minPurchaseQuote", big.NewInt(7))
			},
		},
		{
			name: "an income distributor rewired to another vault is drift", drift: true, wantIn: "incomeDistributor.vault",
			mutate: func(t *testing.T, c *perContractClient, p *models.Project) {
				stubView(t, c, common.HexToAddress(p.Addresses.IncomeDistributor), bindings.NewIncomeDistributor().ABI, "vault", newStrategy)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := driftV3Project()
			c := newPerContractClient()
			stubV3Wiring(t, c, p)
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
			if tc.wantIn != "" && !strings.Contains(reason, tc.wantIn) {
				t.Errorf("reason = %q, want %q", reason, tc.wantIn)
			}
		})
	}
}

func TestVerifyV3ConfigFailsClosedWithoutAStoredOracleConfig(t *testing.T) {
	f := newVerifyFixtureOpts(t, fixtureOpts{v3: true, oracle: true})
	p := verifyV3(t, f)
	if p.Status != models.ProjectStatusActive || p.Oracle == nil {
		t.Fatalf("fixture: Status = %s oracle=%+v", p.Status, p.Oracle)
	}
	check := func(p *models.Project) (bool, string) {
		ok, note, err := verifyV3Config(context.Background(), f.client, p,
			f.vaultAddr, f.tokenAddr, common.HexToAddress(p.Addresses.QuoteToken), f.strategyAddr)
		if err != nil {
			t.Fatal(err)
		}
		return ok, note
	}
	if ok, note := check(p); !ok {
		t.Fatalf("a complete Oracle record failed: %s", note)
	}
	p.Oracle = nil
	if ok, _ := check(p); ok {
		t.Error("an Oracle strategy with no stored config passed verification")
	}
}
