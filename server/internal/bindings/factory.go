package bindings

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// RWAFactory.version() strings that select the deploy / ProjectDeployed encoding
const (
	FactoryVersionV2 = "rwa-v2"
	FactoryVersionV3 = "rwa-v3"
)

// Strategy kinds of IRWAFactory.StrategyKind
const (
	StrategyKindFixed  uint8 = 0
	StrategyKindOracle uint8 = 1
)

// projectConfigV2Fields are rwa-v2's ProjectConfig components; rwa-v3 adds minPurchaseQuote
// through sequencerGracePeriod after redemptionPricePerWholeToken and incomeManager
// after redemptionManager
const (
	projectConfigHeadJSON = `
  {"name":"name","type":"string"},
  {"name":"symbol","type":"string"},
  {"name":"decimals","type":"uint8"},
  {"name":"profileDigest","type":"bytes32"},
  {"name":"projectId","type":"bytes32"},
  {"name":"quoteToken","type":"address"},
  {"name":"purchasePricePerWholeToken","type":"uint256"},
  {"name":"redemptionPricePerWholeToken","type":"uint256"},`
	projectConfigV3PricingJSON = `
  {"name":"minPurchaseQuote","type":"uint256"},
  {"name":"strategyKind","type":"uint8"},
  {"name":"priceFeed","type":"address"},
  {"name":"priceMaxAge","type":"uint64"},
  {"name":"purchaseSpreadBps","type":"uint16"},
  {"name":"redemptionSpreadBps","type":"uint16"},
  {"name":"sequencerUptimeFeed","type":"address"},
  {"name":"sequencerGracePeriod","type":"uint64"},`
	projectConfigRolesJSON = `
  {"name":"redemptionTimeout","type":"uint64"},
  {"name":"admin","type":"address"},
  {"name":"auditor","type":"address"},
  {"name":"complianceOperator","type":"address"},
  {"name":"pricer","type":"address"},
  {"name":"treasurer","type":"address"},
  {"name":"redemptionManager","type":"address"},`
	projectConfigV3IncomeJSON = `
  {"name":"incomeManager","type":"address"},`
	projectConfigTailJSON = `
  {"name":"treasury","type":"address"},
  {"name":"adminTransferDelay","type":"uint48"}`
)

const projectConfigV2TupleJSON = `{"name":"config","type":"tuple","components":[` +
	projectConfigHeadJSON + projectConfigRolesJSON + projectConfigTailJSON + `]}`

const projectConfigTupleJSON = `{"name":"config","type":"tuple","components":[` +
	projectConfigHeadJSON + projectConfigV3PricingJSON + projectConfigRolesJSON +
	projectConfigV3IncomeJSON + projectConfigTailJSON + `]}`

const deploymentV2ComponentsJSON = `
  {"name":"token","type":"address"},
  {"name":"compliance","type":"address"},
  {"name":"supplyController","type":"address"},
  {"name":"vault","type":"address"},
  {"name":"redemptionEscrow","type":"address"},
  {"name":"strategy","type":"address"}`

const deploymentV2TupleJSON = `{"name":"","type":"tuple","components":[` + deploymentV2ComponentsJSON + `]}`

const deploymentTupleJSON = `{"name":"","type":"tuple","components":[` + deploymentV2ComponentsJSON + `,
  {"name":"incomeDistributor","type":"address"}]}`

const projectDeployedHeadJSON = `
    {"name":"projectId","type":"bytes32","indexed":true},
    {"name":"profileDigest","type":"bytes32","indexed":true},
    {"name":"token","type":"address","indexed":false},
    {"name":"compliance","type":"address","indexed":false},
    {"name":"supplyController","type":"address","indexed":false},
    {"name":"vault","type":"address","indexed":false},
    {"name":"redemptionEscrow","type":"address","indexed":false},
    {"name":"strategy","type":"address","indexed":false}`

const factoryABIJSON = `[
  {"type":"function","name":"deploy","stateMutability":"nonpayable",
    "inputs":[` + projectConfigTupleJSON + `],"outputs":[` + deploymentTupleJSON + `]},
  {"type":"function","name":"version","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"string"}]},
  {"type":"function","name":"complianceTokenDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"marketDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"supplyControllerDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"redemptionEscrowDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"incomeDistributorDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"event","name":"ProjectDeployed","anonymous":false,"inputs":[` + projectDeployedHeadJSON + `,
    {"name":"incomeDistributor","type":"address","indexed":false},
    {"name":"version","type":"string","indexed":false}]}
]`

// factoryV2ABIJSON preserves rwa-v2 deploy/ProjectDeployed encodings, checked by
// cmd/verifyabi against the compiled ABI in internal/bindings/testdata/rwa-v2
const factoryV2ABIJSON = `[
  {"type":"function","name":"deploy","stateMutability":"nonpayable",
    "inputs":[` + projectConfigV2TupleJSON + `],"outputs":[` + deploymentV2TupleJSON + `]},
  {"type":"function","name":"version","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"string"}]},
  {"type":"event","name":"ProjectDeployed","anonymous":false,"inputs":[` + projectDeployedHeadJSON + `,
    {"name":"version","type":"string","indexed":false}]}
]`

// Factory binds IRWAFactory and RWAFactory's extra deployer-address getters.
// ABI encodes rwa-v3; ABIV2 preserves rwa-v2 deploy and ProjectDeployed decoding.
type Factory struct{ ABI, ABIV2 abi.ABI }

// NewFactory parses the rwa-v3 and rwa-v2 RWAFactory ABIs
func NewFactory() Factory {
	return Factory{ABI: mustABI(factoryABIJSON), ABIV2: mustABI(factoryV2ABIJSON)}
}

// ProjectConfig mirrors the rwa-v3 IRWAFactory.ProjectConfig
type ProjectConfig struct {
	Name                         string         `abi:"name"`
	Symbol                       string         `abi:"symbol"`
	Decimals                     uint8          `abi:"decimals"`
	ProfileDigest                [32]byte       `abi:"profileDigest"`
	ProjectID                    [32]byte       `abi:"projectId"`
	QuoteToken                   common.Address `abi:"quoteToken"`
	PurchasePricePerWholeToken   *big.Int       `abi:"purchasePricePerWholeToken"`
	RedemptionPricePerWholeToken *big.Int       `abi:"redemptionPricePerWholeToken"`
	MinPurchaseQuote             *big.Int       `abi:"minPurchaseQuote"`
	StrategyKind                 uint8          `abi:"strategyKind"`
	PriceFeed                    common.Address `abi:"priceFeed"`
	PriceMaxAge                  uint64         `abi:"priceMaxAge"`
	PurchaseSpreadBps            uint16         `abi:"purchaseSpreadBps"`
	RedemptionSpreadBps          uint16         `abi:"redemptionSpreadBps"`
	SequencerUptimeFeed          common.Address `abi:"sequencerUptimeFeed"`
	SequencerGracePeriod         uint64         `abi:"sequencerGracePeriod"`
	RedemptionTimeout            uint64         `abi:"redemptionTimeout"`
	Admin                        common.Address `abi:"admin"`
	Auditor                      common.Address `abi:"auditor"`
	ComplianceOperator           common.Address `abi:"complianceOperator"`
	Pricer                       common.Address `abi:"pricer"`
	Treasurer                    common.Address `abi:"treasurer"`
	RedemptionManager            common.Address `abi:"redemptionManager"`
	IncomeManager                common.Address `abi:"incomeManager"`
	Treasury                     common.Address `abi:"treasury"`
	AdminTransferDelay           *big.Int       `abi:"adminTransferDelay"` // uint48, packed/unpacked as *big.Int by go-ethereum
}

// Deployment mirrors the rwa-v3 IRWAFactory.Deployment
type Deployment struct {
	Token             common.Address `abi:"token"`
	Compliance        common.Address `abi:"compliance"`
	SupplyController  common.Address `abi:"supplyController"`
	Vault             common.Address `abi:"vault"`
	RedemptionEscrow  common.Address `abi:"redemptionEscrow"`
	Strategy          common.Address `abi:"strategy"`
	IncomeDistributor common.Address `abi:"incomeDistributor"`
}

// projectConfigV2 is rwa-v2's ProjectConfig, which lacks the rwa-v3 pricing and income
// fields, for packing and decoding rwa-v2 calldata
type projectConfigV2 struct {
	Name                         string         `abi:"name"`
	Symbol                       string         `abi:"symbol"`
	Decimals                     uint8          `abi:"decimals"`
	ProfileDigest                [32]byte       `abi:"profileDigest"`
	ProjectID                    [32]byte       `abi:"projectId"`
	QuoteToken                   common.Address `abi:"quoteToken"`
	PurchasePricePerWholeToken   *big.Int       `abi:"purchasePricePerWholeToken"`
	RedemptionPricePerWholeToken *big.Int       `abi:"redemptionPricePerWholeToken"`
	RedemptionTimeout            uint64         `abi:"redemptionTimeout"`
	Admin                        common.Address `abi:"admin"`
	Auditor                      common.Address `abi:"auditor"`
	ComplianceOperator           common.Address `abi:"complianceOperator"`
	Pricer                       common.Address `abi:"pricer"`
	Treasurer                    common.Address `abi:"treasurer"`
	RedemptionManager            common.Address `abi:"redemptionManager"`
	Treasury                     common.Address `abi:"treasury"`
	AdminTransferDelay           *big.Int       `abi:"adminTransferDelay"`
}

// PackDeploy builds calldata for deploy(ProjectConfig).
func (f Factory) PackDeploy(cfg ProjectConfig) ([]byte, error) {
	return f.ABI.Pack("deploy", cfg)
}

// PackDeployV2 builds rwa-v2 fixture calldata, omitting cfg's rwa-v3 fields. The explicit
// copy avoids go-ethereum rejecting abi tags absent from the tuple.
func (f Factory) PackDeployV2(cfg ProjectConfig) ([]byte, error) {
	return f.ABIV2.Pack("deploy", projectConfigV2{
		Name: cfg.Name, Symbol: cfg.Symbol, Decimals: cfg.Decimals, ProfileDigest: cfg.ProfileDigest,
		ProjectID: cfg.ProjectID, QuoteToken: cfg.QuoteToken,
		PurchasePricePerWholeToken: cfg.PurchasePricePerWholeToken, RedemptionPricePerWholeToken: cfg.RedemptionPricePerWholeToken,
		RedemptionTimeout: cfg.RedemptionTimeout, Admin: cfg.Admin, Auditor: cfg.Auditor,
		ComplianceOperator: cfg.ComplianceOperator, Pricer: cfg.Pricer, Treasurer: cfg.Treasurer,
		RedemptionManager: cfg.RedemptionManager, Treasury: cfg.Treasury, AdminTransferDelay: cfg.AdminTransferDelay,
	})
}

// DeployCall pairs decoded ProjectConfig with its selector's factory generation.
// rwa-v2 leaves v3-only fields zero: Fixed strategy and no minimum purchase.
type DeployCall struct {
	ProjectConfig
	// Version is FactoryVersionV2 or FactoryVersionV3
	Version string
}

// UnpackDeployInput decodes rwa-v3 or rwa-v2 ProjectConfig by its four-byte selector,
// rejecting wrapped and unrelated calls. The server verifies signed roles, treasury,
// auditor and prices from calldata because ProjectDeployed omits them.
func (f Factory) UnpackDeployInput(calldata []byte) (DeployCall, error) {
	if len(calldata) < 4 {
		return DeployCall{}, fmt.Errorf("bindings: deploy calldata is %d bytes, too short for a selector", len(calldata))
	}
	v3, v2 := f.ABI.Methods["deploy"], f.ABIV2.Methods["deploy"]
	switch {
	case bytes.Equal(calldata[:4], v3.ID):
		args, err := v3.Inputs.Unpack(calldata[4:])
		if err != nil {
			return DeployCall{}, err
		}
		if len(args) == 0 {
			return DeployCall{}, fmt.Errorf("bindings: deploy calldata decoded to no arguments")
		}
		cfg := abi.ConvertType(args[0], new(ProjectConfig)).(*ProjectConfig)
		return DeployCall{ProjectConfig: *cfg, Version: FactoryVersionV3}, nil
	case bytes.Equal(calldata[:4], v2.ID):
		args, err := v2.Inputs.Unpack(calldata[4:])
		if err != nil {
			return DeployCall{}, err
		}
		if len(args) == 0 {
			return DeployCall{}, fmt.Errorf("bindings: deploy calldata decoded to no arguments")
		}
		old := abi.ConvertType(args[0], new(projectConfigV2)).(*projectConfigV2)
		// abi.ConvertType copies struct fields by position, so the shorter rwa-v2 tuple
		// cannot convert into ProjectConfig directly
		return DeployCall{ProjectConfig: ProjectConfig{
			Name: old.Name, Symbol: old.Symbol, Decimals: old.Decimals,
			ProfileDigest: old.ProfileDigest, ProjectID: old.ProjectID, QuoteToken: old.QuoteToken,
			PurchasePricePerWholeToken: old.PurchasePricePerWholeToken, RedemptionPricePerWholeToken: old.RedemptionPricePerWholeToken,
			RedemptionTimeout: old.RedemptionTimeout, Admin: old.Admin, Auditor: old.Auditor,
			ComplianceOperator: old.ComplianceOperator, Pricer: old.Pricer, Treasurer: old.Treasurer,
			RedemptionManager: old.RedemptionManager, Treasury: old.Treasury, AdminTransferDelay: old.AdminTransferDelay,
		}, Version: FactoryVersionV2}, nil
	default:
		return DeployCall{}, fmt.Errorf("bindings: calldata selector 0x%x is not RWAFactory.deploy (rwa-v3 0x%x, rwa-v2 0x%x)", calldata[:4], v3.ID, v2.ID)
	}
}

// UnpackDeploy decodes the Deployment return value of deploy().
func (f Factory) UnpackDeploy(data []byte) (Deployment, error) {
	out, err := f.ABI.Unpack("deploy", data)
	if err != nil {
		return Deployment{}, err
	}
	dep := abi.ConvertType(out[0], new(Deployment)).(*Deployment)
	return *dep, nil
}

// ProjectDeployedEvent mirrors the ProjectDeployed event of either factory generation.
// IncomeDistributor is the zero address for rwa-v2.
type ProjectDeployedEvent struct {
	ProjectID         common.Hash
	ProfileDigest     common.Hash
	Token             common.Address
	Compliance        common.Address
	SupplyController  common.Address
	Vault             common.Address
	RedemptionEscrow  common.Address
	Strategy          common.Address
	IncomeDistributor common.Address
	// Version is the event's own version string; Layout is the factory generation
	// (FactoryVersionV2 or FactoryVersionV3) whose topic0 and field layout the log used
	Version string
	Layout  string
}

// IsProjectDeployed reports whether topic0 is the rwa-v3 or rwa-v2 ProjectDeployed event
func (f Factory) IsProjectDeployed(topic0 common.Hash) bool {
	return topic0 == f.ABI.Events["ProjectDeployed"].ID || topic0 == f.ABIV2.Events["ProjectDeployed"].ID
}

// UnpackProjectDeployed decodes a ProjectDeployed log, choosing the rwa-v3 or rwa-v2
// layout by topic0 and records it in Layout; callers compare Layout with Version
func (f Factory) UnpackProjectDeployed(data []byte, topics []common.Hash) (ProjectDeployedEvent, error) {
	if len(topics) == 0 {
		return ProjectDeployedEvent{}, fmt.Errorf("bindings: ProjectDeployed log has no topics")
	}
	var partial struct {
		Token             common.Address
		Compliance        common.Address
		SupplyController  common.Address
		Vault             common.Address
		RedemptionEscrow  common.Address
		Strategy          common.Address
		IncomeDistributor common.Address
		Version           string
	}
	want := FactoryVersionV3
	switch topics[0] {
	case f.ABI.Events["ProjectDeployed"].ID:
		if err := f.ABI.UnpackIntoInterface(&partial, "ProjectDeployed", data); err != nil {
			return ProjectDeployedEvent{}, err
		}
	case f.ABIV2.Events["ProjectDeployed"].ID:
		want = FactoryVersionV2
		if err := f.ABIV2.UnpackIntoInterface(&partial, "ProjectDeployed", data); err != nil {
			return ProjectDeployedEvent{}, err
		}
	default:
		return ProjectDeployedEvent{}, fmt.Errorf("bindings: topic0 %s is not a ProjectDeployed event", topics[0].Hex())
	}
	ev := ProjectDeployedEvent{
		Token: partial.Token, Compliance: partial.Compliance, SupplyController: partial.SupplyController,
		Vault: partial.Vault, RedemptionEscrow: partial.RedemptionEscrow, Strategy: partial.Strategy,
		IncomeDistributor: partial.IncomeDistributor, Version: partial.Version, Layout: want,
	}
	if len(topics) >= 3 {
		ev.ProjectID = topics[1]
		ev.ProfileDigest = topics[2]
	}
	return ev, nil
}

// EventID returns the rwa-v3 keccak256 topic0 for the named event
func (f Factory) EventID(name string) common.Hash { return f.ABI.Events[name].ID }
