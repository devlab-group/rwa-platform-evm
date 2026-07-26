package project

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

const sampleProjectUUID = "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61"

// testChainID is the chain every fixture below signs and verifies against.
const testChainID int64 = 31337

// adminKey/attackerKey are the two wallets the adoption tests need: the
// configured admin whose signature authorizes a deployment, and an unrelated
// wallet standing in for anyone else who can call the permissionless factory.
// (anvil accounts 0 and 1.)
var (
	adminKey    = mustKey("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	attackerKey = mustKey("59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d")
)

func mustKey(hexKey string) *ecdsa.PrivateKey {
	k, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		panic(err)
	}
	return k
}

func keyAddress(k *ecdsa.PrivateKey) common.Address { return crypto.PubkeyToAddress(k.PublicKey) }

// signDeployTx builds the observed deploy transaction — to the factory,
// carrying the ProjectConfig as calldata — signed by key. Adoption recovers the
// sender from this signature, so the fixtures must produce genuinely signed
// transactions rather than bare unsigned ones.
func signDeployTx(t *testing.T, key *ecdsa.PrivateKey, factoryAddr common.Address, calldata []byte, nonce uint64) *types.Transaction {
	t.Helper()
	tx := types.NewTx(&types.LegacyTx{Nonce: nonce, To: &factoryAddr, Gas: 1_000_000, GasPrice: big.NewInt(1), Data: calldata})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(testChainID)), key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func sampleProfileDigestHex() string { return "0x" + strings.Repeat("11", 32) }

// sampleConfig is the ProjectConfig the admin's wallet would have signed and
// submitted to RWAFactory.deploy — the exact struct VerifyDeployment recovers
// from the observed transaction's calldata as its verification allowlist.
func sampleConfig(t *testing.T) bindings.ProjectConfig {
	t.Helper()
	pid, err := projectIDToBytes32(sampleProjectUUID)
	if err != nil {
		t.Fatal(err)
	}
	pd, err := hexToBytes32(sampleProfileDigestHex())
	if err != nil {
		t.Fatal(err)
	}
	return bindings.ProjectConfig{
		Name: "Gold Token", Symbol: "GLD", Decimals: 18,
		ProfileDigest: pd, ProjectID: pid,
		QuoteToken:                   common.HexToAddress("0x000000000000000000000000000000000000AA01"),
		PurchasePricePerWholeToken:   big.NewInt(2000000),
		RedemptionPricePerWholeToken: big.NewInt(1950000),
		RedemptionTimeout:            1209600,
		Admin:                        keyAddress(adminKey),
		Auditor:                      common.HexToAddress("0x000000000000000000000000000000000000AA03"),
		ComplianceOperator:           common.HexToAddress("0x000000000000000000000000000000000000AA04"),
		Pricer:                       common.HexToAddress("0x000000000000000000000000000000000000AA05"),
		Treasurer:                    common.HexToAddress("0x000000000000000000000000000000000000AA06"),
		RedemptionManager:            common.HexToAddress("0x000000000000000000000000000000000000AA07"),
		Treasury:                     common.HexToAddress("0x000000000000000000000000000000000000AA08"),
		AdminTransferDelay:           big.NewInt(0),
	}
}

func sampleProfile() *models.AssetProfile {
	return &models.AssetProfile{ProjectID: sampleProjectUUID, Digest: sampleProfileDigestHex(), TokenDecimals: 18, TokenUnit: "gram"}
}

// verifyFixture bundles a fully-wired, PASSING VerifyDeployment scenario —
// every check configured to succeed against the observed deploy tx + stored
// profile — so a test can mutate exactly one thing and confirm it (and only
// it) turns the result Failed.
type verifyFixture struct {
	svc                                                    *Service
	client                                                 *perContractClient
	projectRepo                                            *memory.ProjectRepository
	profileRepo                                            *memory.AssetProfileRepository
	txHash                                                 common.Hash
	factoryAddr, tokenAddr, complianceAddr, controllerAddr common.Address
	vaultAddr, escrowAddr, strategyAddr                    common.Address
	cfg                                                    bindings.ProjectConfig
	profile                                                *models.AssetProfile
}

func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	factoryAddr := common.HexToAddress("0x0000000000000000000000000000000000FACE")
	client := newPerContractClient()
	projectRepo := memory.NewProjectRepository()
	profileRepo := memory.NewAssetProfileRepository()
	svc := New(client, factoryAddr, projectRepo, testChainID, keyAddress(adminKey))

	cfg := sampleConfig(t)
	profile := sampleProfile()
	if err := profileRepo.Create(context.Background(), profile); err != nil {
		t.Fatal(err)
	}

	tokenAddr := common.HexToAddress("0x0000000000000000000000000000000000B001")
	complianceAddr := common.HexToAddress("0xB002")
	controllerAddr := common.HexToAddress("0xB003")
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000B004")
	escrowAddr := common.HexToAddress("0xB005")
	strategyAddr := common.HexToAddress("0xB006")

	factoryABI := bindings.NewFactory()
	event := factoryABI.ABI.Events["ProjectDeployed"]
	packed, err := event.Inputs.NonIndexed().Pack(tokenAddr, complianceAddr, controllerAddr, vaultAddr, escrowAddr, strategyAddr, "rwa-v2")
	if err != nil {
		t.Fatal(err)
	}

	// The observed deploy transaction: to the factory, carrying the signed
	// ProjectConfig as calldata, broadcast by the admin's own wallet.
	calldata, err := factoryABI.PackDeploy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tx := signDeployTx(t, adminKey, factoryAddr, calldata, 0)
	txHash := tx.Hash()
	client.SetTx(txHash, tx)
	client.SetReceipt(txHash, &types.Receipt{
		Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(1),
		Logs: []*types.Log{{
			Address: factoryAddr,
			Topics:  []common.Hash{event.ID, common.Hash(cfg.ProjectID), common.Hash(cfg.ProfileDigest)},
			Data:    packed,
		}},
	})

	for _, a := range []common.Address{tokenAddr, complianceAddr, controllerAddr, vaultAddr, escrowAddr, strategyAddr, factoryAddr} {
		client.Code[a] = []byte{0x60, 0x60}
	}
	stubExpectedRoleHolders(t, client, &models.Project{
		Addresses: models.Addresses{
			Token: tokenAddr.Hex(), Compliance: complianceAddr.Hex(), SupplyController: controllerAddr.Hex(),
			Vault: vaultAddr.Hex(), RedemptionEscrow: escrowAddr.Hex(), Strategy: strategyAddr.Hex(),
		},
		Admin: cfg.Admin.Hex(), ComplianceOperator: cfg.ComplianceOperator.Hex(),
		Pricer: cfg.Pricer.Hex(), Treasurer: cfg.Treasurer.Hex(), RedemptionManager: cfg.RedemptionManager.Hex(),
	})

	cr := bindings.NewComplianceRegistry()
	isSystemAddressTrue, err := cr.ABI.Methods["isSystemAddress"].Outputs.Pack(true)
	if err != nil {
		t.Fatal(err)
	}
	allowedRecord, err := cr.ABI.Methods["getRecord"].Outputs.Pack(struct {
		Status     uint8
		ValidUntil uint64
	}{Status: uint8(bindings.ComplianceStatusAllowed), ValidUntil: 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, sys := range []common.Address{vaultAddr, escrowAddr} {
		isSysData, err := cr.ABI.Pack("isSystemAddress", sys)
		if err != nil {
			t.Fatal(err)
		}
		client.CallResponses[common.Bytes2Hex(isSysData)] = isSystemAddressTrue
		recData, err := cr.PackGetRecord(sys)
		if err != nil {
			t.Fatal(err)
		}
		client.CallResponses[common.Bytes2Hex(recData)] = allowedRecord
	}

	setViewResponse := func(contractABI abi.ABI, method string, value any) {
		data, err := contractABI.Pack(method)
		if err != nil {
			t.Fatal(err)
		}
		out, err := contractABI.Methods[method].Outputs.Pack(value)
		if err != nil {
			t.Fatal(err)
		}
		client.CallResponses[common.Bytes2Hex(data)] = out
	}
	tokenABI := bindings.NewERC20().ABI
	setViewResponse(tokenABI, "compliance", complianceAddr)
	setViewResponse(tokenABI, "supplyController", controllerAddr)
	setViewResponse(tokenABI, "redemptionEscrow", escrowAddr)
	setViewResponse(tokenABI, "decimals", cfg.Decimals)
	vaultABI := bindings.NewVault().ABI
	setViewResponse(vaultABI, "token", tokenAddr)
	setViewResponse(vaultABI, "quoteToken", cfg.QuoteToken)
	setViewResponse(vaultABI, "treasury", cfg.Treasury)
	setViewResponse(vaultABI, "strategy", strategyAddr)
	escrowABI := bindings.NewRedemptionEscrow().ABI
	setViewResponse(escrowABI, "token", tokenAddr)
	setViewResponse(escrowABI, "quoteToken", cfg.QuoteToken)
	setViewResponse(escrowABI, "vault", vaultAddr)
	setViewResponse(escrowABI, "strategy", strategyAddr)
	setViewResponse(escrowABI, "redemptionTimeout", cfg.RedemptionTimeout)
	controllerABI := bindings.NewSupplyController().ABI
	setViewResponse(controllerABI, "vault", vaultAddr)
	setViewResponse(controllerABI, "token", tokenAddr)
	setViewResponse(controllerABI, "auditor", cfg.Auditor)
	setViewResponse(controllerABI, "profileDigest", cfg.ProfileDigest)
	strategyABI := bindings.NewFixedPriceStrategy().ABI
	setViewResponse(strategyABI, "tokenDecimals", cfg.Decimals)
	setViewResponse(strategyABI, "purchasePricePerWholeToken", cfg.PurchasePricePerWholeToken)
	setViewResponse(strategyABI, "redemptionPricePerWholeToken", cfg.RedemptionPricePerWholeToken)

	versionData, err := bindings.NewFactory().ABI.Pack("version")
	if err != nil {
		t.Fatal(err)
	}
	versionOut, err := bindings.NewFactory().ABI.Methods["version"].Outputs.Pack("rwa-v2")
	if err != nil {
		t.Fatal(err)
	}
	client.CallResponses[common.Bytes2Hex(versionData)] = versionOut

	return &verifyFixture{
		svc: svc, client: client, projectRepo: projectRepo, profileRepo: profileRepo, txHash: txHash, factoryAddr: factoryAddr,
		tokenAddr: tokenAddr, complianceAddr: complianceAddr, controllerAddr: controllerAddr,
		vaultAddr: vaultAddr, escrowAddr: escrowAddr, strategyAddr: strategyAddr, cfg: cfg, profile: profile,
	}
}

// seedDeployEvent records the fixture's ProjectDeployed log into chainEvents
// (as the indexer would), so ReconcileDeployment can find and adopt it.
func (f *verifyFixture) seedDeployEvent(t *testing.T, chainEvents *memory.ChainEventRepository) {
	t.Helper()
	if err := chainEvents.Create(context.Background(), &models.ChainEvent{
		ChainID: 31337, Address: f.factoryAddr.Hex(), TxHash: f.txHash.Hex(), LogIndex: 0, BlockNumber: 1,
		Name: "ProjectDeployed",
		Data: map[string]any{
			"projectId":     common.Hash(f.cfg.ProjectID).Hex(),
			"profileDigest": common.Hash(f.cfg.ProfileDigest).Hex(),
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDeploymentRecordsAddressesAndActivates(t *testing.T) {
	f := newVerifyFixture(t)
	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusActive {
		t.Errorf("Status = %s, want Active (reason: %s)", updated.Status, updated.VerificationNote)
	}
	if updated.Addresses.Token != f.tokenAddr.Hex() {
		t.Errorf("Token = %s, want %s", updated.Addresses.Token, f.tokenAddr.Hex())
	}
	if updated.Addresses.QuoteToken != f.cfg.QuoteToken.Hex() {
		t.Errorf("QuoteToken = %s, want %s", updated.Addresses.QuoteToken, f.cfg.QuoteToken.Hex())
	}
	if updated.Version != "rwa-v2" {
		t.Errorf("Version = %q", updated.Version)
	}
	if updated.DeployTxHash != f.txHash.Hex() {
		t.Errorf("DeployTxHash = %s, want %s", updated.DeployTxHash, f.txHash.Hex())
	}
	if updated.Auditor != f.cfg.Auditor.Hex() {
		t.Errorf("Auditor = %s, want the calldata's %s", updated.Auditor, f.cfg.Auditor.Hex())
	}
	if !updated.BytecodeVerified {
		t.Error("expected BytecodeVerified = true")
	}
	if len(updated.Roles["DEFAULT_ADMIN_ROLE"]) == 0 {
		t.Error("expected DEFAULT_ADMIN_ROLE to have at least one holder")
	}
}

func stubQuoteTokenDecimals(t *testing.T, c *perContractClient, quoteToken common.Address, dec uint8) {
	t.Helper()
	erc20 := bindings.NewERC20()
	data, err := erc20.PackDecimals()
	if err != nil {
		t.Fatal(err)
	}
	out, err := erc20.ABI.Methods["decimals"].Outputs.Pack(dec)
	if err != nil {
		t.Fatal(err)
	}
	m := c.byContract[quoteToken]
	if m == nil {
		m = map[string][]byte{}
		c.byContract[quoteToken] = m
	}
	m[common.Bytes2Hex(data)] = out
}

func TestVerifyDeploymentPersistsQuoteTokenDecimals(t *testing.T) {
	f := newVerifyFixture(t)
	stubQuoteTokenDecimals(t, f.client, f.cfg.QuoteToken, 6)

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s, want Active (reason: %s)", updated.Status, updated.VerificationNote)
	}
	if updated.QuoteDecimals != 6 {
		t.Errorf("QuoteDecimals = %d, want 6", updated.QuoteDecimals)
	}
}

func TestVerifyDeploymentLeavesQuoteDecimalsUnsetWhenUnreadable(t *testing.T) {
	f := newVerifyFixture(t)
	erc20 := bindings.NewERC20()
	data, err := erc20.PackDecimals()
	if err != nil {
		t.Fatal(err)
	}
	f.client.byContract[f.cfg.QuoteToken] = map[string][]byte{common.Bytes2Hex(data): {}}

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s, want Active (reason: %s)", updated.Status, updated.VerificationNote)
	}
	if updated.QuoteDecimals != 0 {
		t.Errorf("QuoteDecimals = %d, want 0 (unset)", updated.QuoteDecimals)
	}
}

func TestVerifyDeploymentFailsOnRevertedReceipt(t *testing.T) {
	f := newVerifyFixture(t)
	receipt, err := f.client.TransactionReceipt(context.Background(), f.txHash)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = types.ReceiptStatusFailed

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnWrongLogEmitter(t *testing.T) {
	f := newVerifyFixture(t)
	receipt, err := f.client.TransactionReceipt(context.Background(), f.txHash)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Logs[0].Address = common.HexToAddress("0x000000000000000000000000000000BADBAD01")

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnProjectIDMismatch(t *testing.T) {
	f := newVerifyFixture(t)
	receipt, err := f.client.TransactionReceipt(context.Background(), f.txHash)
	if err != nil {
		t.Fatal(err)
	}
	wrongProjectID, err := projectIDToBytes32("some-other-project")
	if err != nil {
		t.Fatal(err)
	}
	receipt.Logs[0].Topics[1] = common.Hash(wrongProjectID)

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnUnsupportedVersion(t *testing.T) {
	f := newVerifyFixture(t)
	receipt, err := f.client.TransactionReceipt(context.Background(), f.txHash)
	if err != nil {
		t.Fatal(err)
	}
	event := bindings.NewFactory().ABI.Events["ProjectDeployed"]
	packed, err := event.Inputs.NonIndexed().Pack(f.tokenAddr, f.complianceAddr, f.controllerAddr, f.vaultAddr, f.escrowAddr, f.strategyAddr, "rwa-v99-unknown")
	if err != nil {
		t.Fatal(err)
	}
	receipt.Logs[0].Data = packed

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

// TestVerifyDeploymentFailsOnUndecodableCalldata: the ProjectDeployed event
// binds correctly, but the deploy transaction's calldata isn't a decodable
// RWAFactory.deploy — verification must fail rather than adopt with no
// recoverable role/config allowlist.
func TestVerifyDeploymentFailsOnUndecodableCalldata(t *testing.T) {
	f := newVerifyFixture(t)
	garbage := types.NewTx(&types.LegacyTx{Nonce: 0, To: &f.factoryAddr, Gas: 1_000_000, GasPrice: big.NewInt(1), Data: []byte{0x01, 0x02, 0x03, 0x04}})
	f.client.SetTx(f.txHash, garbage)

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

// TestVerifyDeploymentFailsOnCalldataProfileMismatch: the deployer submitted a
// ProjectConfig whose decimals disagree with the stored profile — the signed
// calldata must be cross-checked against the profile binding.
func TestVerifyDeploymentFailsOnCalldataProfileMismatch(t *testing.T) {
	f := newVerifyFixture(t)
	badCfg := f.cfg
	badCfg.Decimals = 6 // profile says 18
	calldata, err := bindings.NewFactory().PackDeploy(badCfg)
	if err != nil {
		t.Fatal(err)
	}
	f.client.SetTx(f.txHash, types.NewTx(&types.LegacyTx{Nonce: 0, To: &f.factoryAddr, Gas: 1_000_000, GasPrice: big.NewInt(1), Data: calldata}))

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnMissingRequiredRole(t *testing.T) {
	f := newVerifyFixture(t)
	stubRoleHolders(t, f.client, f.tokenAddr, bindings.DefaultAdminRole)

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnUnexpectedRoleHolder(t *testing.T) {
	f := newVerifyFixture(t)
	attacker := common.HexToAddress("0x000000000000000000000000000000000000DEAD")
	stubRoleHolders(t, f.client, f.tokenAddr, bindings.DefaultAdminRole, f.cfg.Admin, attacker)

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed (unexpected role holder must block Active)", updated.Status)
	}
}

func TestVerifyDeploymentFailsWhenFactoryRetainsRole(t *testing.T) {
	f := newVerifyFixture(t)
	hasRoleTrue, err := bindings.NewAccessControl().ABI.Methods["hasRole"].Outputs.Pack(true)
	if err != nil {
		t.Fatal(err)
	}
	factoryRoleCall, err := bindings.NewAccessControl().PackHasRole(bindings.DefaultAdminRole, f.factoryAddr)
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(factoryRoleCall)] = hasRoleTrue

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnMissingSystemAllowlist(t *testing.T) {
	f := newVerifyFixture(t)
	cr := bindings.NewComplianceRegistry()
	isSystemAddressFalse, err := cr.ABI.Methods["isSystemAddress"].Outputs.Pack(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := cr.ABI.Pack("isSystemAddress", f.vaultAddr)
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(data)] = isSystemAddressFalse

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnSystemAddressWithFiniteExpiry(t *testing.T) {
	f := newVerifyFixture(t)
	cr := bindings.NewComplianceRegistry()
	finiteRecord, err := cr.ABI.Methods["getRecord"].Outputs.Pack(struct {
		Status     uint8
		ValidUntil uint64
	}{Status: uint8(bindings.ComplianceStatusAllowed), ValidUntil: 1893456000})
	if err != nil {
		t.Fatal(err)
	}
	data, err := cr.PackGetRecord(f.escrowAddr)
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(data)] = finiteRecord

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsWhenFactoryRetainsComplianceRole(t *testing.T) {
	f := newVerifyFixture(t)
	hasRoleTrue, err := bindings.NewAccessControl().ABI.Methods["hasRole"].Outputs.Pack(true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := bindings.NewAccessControl().PackHasRole(bindings.ComplianceRole, f.factoryAddr)
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(data)] = hasRoleTrue

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnLiveVersionMismatch(t *testing.T) {
	f := newVerifyFixture(t)
	versionData, err := bindings.NewFactory().ABI.Pack("version")
	if err != nil {
		t.Fatal(err)
	}
	versionOut, err := bindings.NewFactory().ABI.Methods["version"].Outputs.Pack("rwa-v2-unannounced")
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(versionData)] = versionOut

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentActiveClearsVerificationNote(t *testing.T) {
	f := newVerifyFixture(t)
	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s, want Active", updated.Status)
	}
	if updated.VerificationNote != "" {
		t.Errorf("VerificationNote = %q, want empty on a fully verified deployment", updated.VerificationNote)
	}
}

func TestVerifyDeploymentFailsOnImmutableConfigMismatch(t *testing.T) {
	f := newVerifyFixture(t)
	vaultABI := bindings.NewVault().ABI
	data, err := vaultABI.Pack("quoteToken")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := vaultABI.Methods["quoteToken"].Outputs.Pack(common.HexToAddress("0x000000000000000000000000000000BADBAD02"))
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(data)] = wrong

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

func TestVerifyDeploymentFailsOnRedemptionEscrowPointerMismatch(t *testing.T) {
	f := newVerifyFixture(t)
	tokenABI := bindings.NewERC20().ABI
	data, err := tokenABI.Pack("redemptionEscrow")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := tokenABI.Methods["redemptionEscrow"].Outputs.Pack(common.HexToAddress("0x000000000000000000000000000000BADE5C0A"))
	if err != nil {
		t.Fatal(err)
	}
	f.client.CallResponses[common.Bytes2Hex(data)] = wrong

	updated, err := f.svc.VerifyDeployment(context.Background(), f.txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Errorf("Status = %s, want Failed", updated.Status)
	}
}

// --- ReconcileDeployment (observe-only adoption / reorg demotion) ---

func TestReconcileDeploymentAdoptsMatchingEvent(t *testing.T) {
	f := newVerifyFixture(t)
	chainEvents := memory.NewChainEventRepository()
	f.seedDeployEvent(t, chainEvents)

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	p, err := f.svc.GetProject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s, want Active (reason: %s)", p.Status, p.VerificationNote)
	}

	// Idempotent: a second pass is a no-op, still Active.
	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("second ReconcileDeployment: %v", err)
	}
	if p2, _ := f.svc.GetProject(context.Background()); p2.Status != models.ProjectStatusActive {
		t.Errorf("Status after second reconcile = %s, want Active", p2.Status)
	}
}

func TestReconcileDeploymentIgnoresMismatchedEvent(t *testing.T) {
	f := newVerifyFixture(t)
	chainEvents := memory.NewChainEventRepository()
	// An event bound to a DIFFERENT profileDigest than the stored profile.
	if err := chainEvents.Create(context.Background(), &models.ChainEvent{
		ChainID: 31337, Address: f.factoryAddr.Hex(), TxHash: f.txHash.Hex(), LogIndex: 0, BlockNumber: 1,
		Name: "ProjectDeployed",
		Data: map[string]any{
			"projectId":     common.Hash(f.cfg.ProjectID).Hex(),
			"profileDigest": "0x" + strings.Repeat("22", 32),
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	if _, err := f.svc.GetProject(context.Background()); err == nil {
		t.Fatal("expected no project to be adopted for a non-matching ProjectDeployed event")
	}
}

// seedLookAlikeDeploy stages the deployment-hijack attack: a second, fully real deployment
// carrying the SAME (public) projectId and profileDigest as the stored profile,
// broadcast by `key` through the same permissionless factory, at a LATER block
// than the legitimate one — with its own attacker-controlled addresses and
// roles. Returns the look-alike's transaction hash.
func (f *verifyFixture) seedLookAlikeDeploy(t *testing.T, chainEvents *memory.ChainEventRepository, key *ecdsa.PrivateKey, mutate func(*bindings.ProjectConfig)) common.Hash {
	t.Helper()
	cfg := f.cfg
	cfg.Admin = keyAddress(key)
	cfg.Auditor = common.HexToAddress("0x00000000000000000000000000000000000BAD1")
	cfg.Treasury = common.HexToAddress("0x00000000000000000000000000000000000BAD2")
	if mutate != nil {
		mutate(&cfg)
	}

	factoryABI := bindings.NewFactory()
	calldata, err := factoryABI.PackDeploy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tx := signDeployTx(t, key, f.factoryAddr, calldata, 7)

	evilAddr := func(n byte) common.Address {
		return common.BytesToAddress([]byte{0xE0, n})
	}
	event := factoryABI.ABI.Events["ProjectDeployed"]
	packed, err := event.Inputs.NonIndexed().Pack(evilAddr(1), evilAddr(2), evilAddr(3), evilAddr(4), evilAddr(5), evilAddr(6), "rwa-v2")
	if err != nil {
		t.Fatal(err)
	}
	f.client.SetTx(tx.Hash(), tx)
	f.client.SetReceipt(tx.Hash(), &types.Receipt{
		Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(2),
		Logs: []*types.Log{{
			Address: f.factoryAddr,
			Topics:  []common.Hash{event.ID, common.Hash(cfg.ProjectID), common.Hash(cfg.ProfileDigest)},
			Data:    packed,
		}},
	})
	for n := byte(1); n <= 6; n++ {
		f.client.Code[evilAddr(n)] = []byte{0x60, 0x60}
	}
	if chainEvents != nil {
		if err := chainEvents.Create(context.Background(), &models.ChainEvent{
			ChainID: testChainID, Address: f.factoryAddr.Hex(), TxHash: tx.Hash().Hex(), LogIndex: 0, BlockNumber: 2,
			Name: "ProjectDeployed",
			Data: map[string]any{
				"projectId":     common.Hash(cfg.ProjectID).Hex(),
				"profileDigest": common.Hash(cfg.ProfileDigest).Hex(),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return tx.Hash()
}

// TestReconcileDeploymentRejectsLookAlikeFromAnotherDeployer is the
// deployment-hijack regression. RWAFactory.deploy is permissionless and both projectId and
// profileDigest are public, so anyone can deploy a look-alike stack carrying
// them. Adoption previously kept the LATEST matching event and recovered every
// expected role/treasury/auditor value from that transaction's own calldata, so
// the look-alike verified as internally consistent and replaced the live
// project — pointing the SPA and every chain read at attacker contracts. The
// deploy transaction's signer must be the configured admin.
func TestReconcileDeploymentRejectsLookAlikeFromAnotherDeployer(t *testing.T) {
	f := newVerifyFixture(t)
	chainEvents := memory.NewChainEventRepository()
	f.seedDeployEvent(t, chainEvents)
	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	before, _ := f.svc.GetProject(context.Background())
	if before.Status != models.ProjectStatusActive {
		t.Fatalf("precondition: expected Active, got %s (%s)", before.Status, before.VerificationNote)
	}

	f.seedLookAlikeDeploy(t, chainEvents, attackerKey, nil)

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment with a look-alike present: %v", err)
	}
	after, err := f.svc.GetProject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The live project must be untouched — neither re-pointed at the attacker's
	// stack nor knocked out of Active, which would be a denial of service in
	// place of the takeover.
	if after.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s), want the legitimate project left Active", after.Status, after.VerificationNote)
	}
	if !strings.EqualFold(after.DeployTxHash, f.txHash.Hex()) {
		t.Errorf("DeployTxHash = %s, want the legitimate %s", after.DeployTxHash, f.txHash.Hex())
	}
	if !strings.EqualFold(after.Admin, keyAddress(adminKey).Hex()) {
		t.Errorf("Admin = %s, want the configured admin %s", after.Admin, keyAddress(adminKey).Hex())
	}
	if !strings.EqualFold(after.Addresses.Token, f.tokenAddr.Hex()) {
		t.Errorf("Token = %s, want the legitimate %s", after.Addresses.Token, f.tokenAddr.Hex())
	}
}

// TestReconcileDeploymentIgnoresLookAlikeWithNoLegitimateDeployment: with only
// an unauthorized deployment on chain, nothing is adopted at all — and no
// project record is written, so the attacker cannot even force a Failed record
// into existence.
func TestReconcileDeploymentIgnoresLookAlikeWithNoLegitimateDeployment(t *testing.T) {
	f := newVerifyFixture(t)
	chainEvents := memory.NewChainEventRepository()
	f.seedLookAlikeDeploy(t, chainEvents, attackerKey, nil)

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	if p, err := f.svc.GetProject(context.Background()); err == nil {
		t.Fatalf("adopted an unauthorized deployment: %+v", p)
	}
}

// TestReconcileDeploymentRejectsAdminSentDeployNamingAnotherAdmin covers the
// second half of the binding: the transaction is signed by the configured admin
// but its ProjectConfig hands the roles to someone else, so the resulting stack
// would not be under the operator's control.
func TestReconcileDeploymentRejectsAdminSentDeployNamingAnotherAdmin(t *testing.T) {
	f := newVerifyFixture(t)
	chainEvents := memory.NewChainEventRepository()
	f.seedLookAlikeDeploy(t, chainEvents, adminKey, func(cfg *bindings.ProjectConfig) {
		cfg.Admin = keyAddress(attackerKey)
	})

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	if p, err := f.svc.GetProject(context.Background()); err == nil {
		t.Fatalf("adopted a deployment naming a different admin: %+v", p)
	}
}

// TestVerifyDeploymentFailsForUnauthorizedDeployer: the binding is re-checked
// inside VerifyDeployment, so a direct caller cannot bypass the filter
// ReconcileDeployment applies.
func TestVerifyDeploymentFailsForUnauthorizedDeployer(t *testing.T) {
	f := newVerifyFixture(t)
	txHash := f.seedLookAlikeDeploy(t, nil, attackerKey, nil)

	updated, err := f.svc.VerifyDeployment(context.Background(), txHash, f.profile)
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}
	if updated.Status != models.ProjectStatusFailed {
		t.Fatalf("Status = %s, want Failed for a deploy the configured admin never sent", updated.Status)
	}
	if !strings.Contains(updated.VerificationNote, keyAddress(adminKey).Hex()) {
		t.Errorf("VerificationNote = %q, want it to name the configured admin", updated.VerificationNote)
	}
}

// TestVerifyDeploymentUnauthenticatedWhenNoAdminConfigured: with no admin
// address configured (development — production config refuses to start without
// one) the binding is disabled and the old adopt-anything behavior is kept, so
// existing local setups still work.
func TestVerifyDeploymentUnauthenticatedWhenNoAdminConfigured(t *testing.T) {
	f := newVerifyFixture(t)
	f.svc.adminAddress = common.Address{}
	chainEvents := memory.NewChainEventRepository()
	f.seedDeployEvent(t, chainEvents)

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	p, err := f.svc.GetProject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != models.ProjectStatusActive {
		t.Fatalf("Status = %s (%s), want Active with the binding disabled", p.Status, p.VerificationNote)
	}
}

func TestReconcileDeploymentNoProfileNoOp(t *testing.T) {
	f := newVerifyFixture(t)
	empty := memory.NewAssetProfileRepository()
	chainEvents := memory.NewChainEventRepository()
	f.seedDeployEvent(t, chainEvents)

	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, empty); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	if _, err := f.svc.GetProject(context.Background()); err == nil {
		t.Fatal("expected no adoption without a stored profile")
	}
}

func TestReconcileDeploymentDemotesOnReorg(t *testing.T) {
	f := newVerifyFixture(t)
	chainEvents := memory.NewChainEventRepository()
	f.seedDeployEvent(t, chainEvents)
	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment: %v", err)
	}
	if p, _ := f.svc.GetProject(context.Background()); p.Status != models.ProjectStatusActive {
		t.Fatalf("precondition: expected Active, got %s", p.Status)
	}

	// The adopting event is rolled back (indexer deletes it on reorg).
	if _, err := chainEvents.DeleteFromBlock(context.Background(), 31337, f.factoryAddr.Hex(), 1); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReconcileDeployment(context.Background(), chainEvents, f.profileRepo); err != nil {
		t.Fatalf("ReconcileDeployment (reorg): %v", err)
	}
	p, err := f.svc.GetProject(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != models.ProjectStatusUndeployed {
		t.Errorf("Status after reorg = %s, want Undeployed", p.Status)
	}
	if p.Addresses.Token != "" {
		t.Errorf("expected addresses cleared on demotion, got token %s", p.Addresses.Token)
	}
}
