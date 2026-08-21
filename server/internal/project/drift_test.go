package project

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// stubView makes contract answer a no-argument view call with value.
func stubView(t *testing.T, c *perContractClient, contract common.Address, contractABI abi.ABI, method string, value any) {
	t.Helper()
	data, err := contractABI.Pack(method)
	if err != nil {
		t.Fatalf("pack %s: %v", method, err)
	}
	out, err := contractABI.Methods[method].Outputs.Pack(value)
	if err != nil {
		t.Fatalf("pack %s output: %v", method, err)
	}
	m := c.byContract[contract]
	if m == nil {
		m = map[string][]byte{}
		c.byContract[contract] = m
	}
	m[common.Bytes2Hex(data)] = out
}

func driftProject() *models.Project {
	return &models.Project{
		ProjectID: "p1", Status: models.ProjectStatusActive, Addresses: sampleAddresses(),
		QuoteToken:    "0x0000000000000000000000000000000000C001",
		Treasury:      "0x0000000000000000000000000000000000C002",
		Auditor:       "0x0000000000000000000000000000000000C003",
		ProfileDigest: "0x" + strings.Repeat("11", 32), TokenDecimals: 6, RedemptionTimeout: 1209600,
		// Deploy-time prices; the pricer moves these legitimately, so the
		// drift check must not look at them.
		PurchasePricePerWholeToken: "1000", RedemptionPricePerWholeToken: "900",
	}
}

// stubWiring answers every view CheckConfigDrift makes, all matching p —
// the clean baseline a test then breaks in exactly one place.
func stubWiring(t *testing.T, c *perContractClient, p *models.Project) {
	t.Helper()
	tokenABI := bindings.NewERC20().ABI
	vaultABI := bindings.NewVault().ABI
	escrowABI := bindings.NewRedemptionEscrow().ABI
	controllerABI := bindings.NewSupplyController().ABI
	strategyABI := bindings.NewFixedPriceStrategy().ABI

	a := p.Addresses
	token, compliance := common.HexToAddress(a.Token), common.HexToAddress(a.Compliance)
	vault, escrow := common.HexToAddress(a.Vault), common.HexToAddress(a.RedemptionEscrow)
	controller, strategy := common.HexToAddress(a.SupplyController), common.HexToAddress(a.Strategy)
	quote := common.HexToAddress(p.QuoteToken)

	stubView(t, c, token, tokenABI, "compliance", compliance)
	stubView(t, c, token, tokenABI, "supplyController", controller)
	stubView(t, c, token, tokenABI, "redemptionEscrow", escrow)
	stubView(t, c, token, tokenABI, "decimals", p.TokenDecimals)
	stubView(t, c, vault, vaultABI, "token", token)
	stubView(t, c, vault, vaultABI, "quoteToken", quote)
	stubView(t, c, vault, vaultABI, "treasury", common.HexToAddress(p.Treasury))
	stubView(t, c, vault, vaultABI, "strategy", strategy)
	stubView(t, c, escrow, escrowABI, "token", token)
	stubView(t, c, escrow, escrowABI, "quoteToken", quote)
	stubView(t, c, escrow, escrowABI, "vault", vault)
	stubView(t, c, escrow, escrowABI, "strategy", strategy)
	stubView(t, c, escrow, escrowABI, "redemptionTimeout", uint64(p.RedemptionTimeout))
	stubView(t, c, controller, controllerABI, "vault", vault)
	stubView(t, c, controller, controllerABI, "token", token)
	stubView(t, c, controller, controllerABI, "auditor", common.HexToAddress(p.Auditor))
	var digest [32]byte
	copy(digest[:], common.FromHex(p.ProfileDigest))
	stubView(t, c, controller, controllerABI, "profileDigest", digest)
	stubView(t, c, strategy, strategyABI, "tokenDecimals", p.TokenDecimals)
}

func driftRepo(t *testing.T, p *models.Project) repository.ProjectRepository {
	t.Helper()
	repos := memory.New()
	if err := repos.Projects.Upsert(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return repos.Projects
}

// TestCheckConfigDriftClean: a stack still wired the way the record says is
// not drift.
func TestCheckConfigDriftClean(t *testing.T) {
	p := driftProject()
	c := newPerContractClient()
	stubWiring(t, c, p)

	ok, reason, err := CheckConfigDrift(context.Background(), c, driftRepo(t, p))
	if err != nil {
		t.Fatalf("CheckConfigDrift: %v", err)
	}
	if !ok {
		t.Fatalf("reported drift on a clean stack: %s", reason)
	}
}

// TestCheckConfigDriftDetectsRewiring: this is the whole point — a wiring
// value that changed after adoption, which nothing re-evaluated before.
func TestCheckConfigDriftDetectsRewiring(t *testing.T) {
	p := driftProject()
	c := newPerContractClient()
	stubWiring(t, c, p)
	// The token now points at a different compliance registry.
	stubView(t, c, common.HexToAddress(p.Addresses.Token), bindings.NewERC20().ABI, "compliance",
		common.HexToAddress("0x00000000000000000000000000000000DEAD01"))

	ok, reason, err := CheckConfigDrift(context.Background(), c, driftRepo(t, p))
	if err != nil {
		t.Fatalf("CheckConfigDrift: %v", err)
	}
	if ok {
		t.Fatal("a rewired token.compliance was not reported")
	}
	if !strings.Contains(reason, "token.compliance") {
		t.Errorf("reason = %q, want it to name token.compliance", reason)
	}
}

// TestCheckConfigDriftAcceptsProjectedRotations: setTreasury/setAuditor are
// ordinary admin actions that ReconcileSecurity already tracks. Comparing
// them against the deploy snapshot would report every legitimate rotation as
// drift, which would train an operator to ignore the alert.
func TestCheckConfigDriftAcceptsProjectedRotations(t *testing.T) {
	p := driftProject()
	newTreasury := common.HexToAddress("0x00000000000000000000000000000000BEEF01")
	newAuditor := common.HexToAddress("0x00000000000000000000000000000000BEEF02")
	p.Security = &models.SecurityState{Treasury: newTreasury.Hex(), Auditor: newAuditor.Hex()}

	c := newPerContractClient()
	stubWiring(t, c, p)
	stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "treasury", newTreasury)
	stubView(t, c, common.HexToAddress(p.Addresses.SupplyController), bindings.NewSupplyController().ABI, "auditor", newAuditor)

	ok, reason, err := CheckConfigDrift(context.Background(), c, driftRepo(t, p))
	if err != nil {
		t.Fatalf("CheckConfigDrift: %v", err)
	}
	if !ok {
		t.Fatalf("a rotation the projection already tracks was reported as drift: %s", reason)
	}
}

// TestCheckConfigDriftIgnoresPriceMoves: the pricer moving a price is the
// strategy doing its job, not the stack being rewired.
func TestCheckConfigDriftIgnoresPriceMoves(t *testing.T) {
	p := driftProject()
	c := newPerContractClient()
	stubWiring(t, c, p)
	strategyABI := bindings.NewFixedPriceStrategy().ABI
	strategy := common.HexToAddress(p.Addresses.Strategy)
	stubView(t, c, strategy, strategyABI, "purchasePricePerWholeToken", big.NewInt(7777))
	stubView(t, c, strategy, strategyABI, "redemptionPricePerWholeToken", big.NewInt(6666))

	ok, reason, err := CheckConfigDrift(context.Background(), c, driftRepo(t, p))
	if err != nil {
		t.Fatalf("CheckConfigDrift: %v", err)
	}
	if !ok {
		t.Fatalf("a price move was reported as drift: %s", reason)
	}
}

// TestCheckConfigDriftFlagsVaultEscrowStrategySplit: a strategy swap is
// legitimate for the Vault, but RedemptionEscrow.strategy is immutable and
// cannot follow — so purchases and redemptions end up quoted by different
// contracts. That is a standing condition an operator has to be told about.
func TestCheckConfigDriftFlagsVaultEscrowStrategySplit(t *testing.T) {
	p := driftProject()
	swapped := common.HexToAddress("0x00000000000000000000000000000000B11B01")
	p.Security = &models.SecurityState{Strategy: swapped.Hex()}

	c := newPerContractClient()
	stubWiring(t, c, p)
	// The Vault prices through the new strategy; the escrow still holds the
	// deployed one.
	stubView(t, c, common.HexToAddress(p.Addresses.Vault), bindings.NewVault().ABI, "strategy", swapped)

	ok, reason, err := CheckConfigDrift(context.Background(), c, driftRepo(t, p))
	if err != nil {
		t.Fatalf("CheckConfigDrift: %v", err)
	}
	if ok {
		t.Fatal("a vault/escrow strategy split was not reported")
	}
	if !strings.Contains(reason, "priced by different contracts") {
		t.Errorf("reason = %q, want it to explain the split", reason)
	}
}

// TestCheckConfigDriftSkipsInactiveProject: nothing to compare against
// before a deployment is adopted, and no record at all is not an error.
func TestCheckConfigDriftSkipsInactiveProject(t *testing.T) {
	repos := memory.New()
	if ok, _, err := CheckConfigDrift(context.Background(), newPerContractClient(), repos.Projects); err != nil || !ok {
		t.Fatalf("no project: ok=%v err=%v, want true/nil", ok, err)
	}

	p := driftProject()
	p.Status = models.ProjectStatusDeploying
	if ok, _, err := CheckConfigDrift(context.Background(), newPerContractClient(), driftRepo(t, p)); err != nil || !ok {
		t.Fatalf("deploying project: ok=%v err=%v, want true/nil", ok, err)
	}
}
