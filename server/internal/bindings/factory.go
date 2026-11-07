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
	FactoryVersionV4 = "rwa-v4"
)

// IsV3OrLater reports whether version has the rwa-v3 additions: the income distributor, the
// minimum purchase and the strategy kind in the deploy config
func IsV3OrLater(version string) bool {
	return version == FactoryVersionV3 || version == FactoryVersionV4
}

// ProjectDeployedLayout is the ProjectDeployed event layout of version: rwa-v3 and rwa-v4 share
// one topic0 and field list, so both report FactoryVersionV3
func ProjectDeployedLayout(version string) string {
	if version == FactoryVersionV4 {
		return FactoryVersionV3
	}
	return version
}

// Strategy kinds of IRWAFactory.StrategyKind
const (
	StrategyKindFixed  uint8 = 0
	StrategyKindOracle uint8 = 1
)

// projectConfigHeadJSON through projectConfigTailJSON are rwa-v2's ProjectConfig components
// around redemptionTimeout. rwa-v3 adds minPurchaseQuote through sequencerGracePeriod after
// redemptionPricePerWholeToken and incomeManager after redemptionManager. rwa-v4 adds
// dealingCutoff before redemptionTimeout and purchaseTimeout after it.
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
	projectConfigRedemptionTimeoutJSON = `
  {"name":"redemptionTimeout","type":"uint64"},`
	projectConfigV4DealingCutoffJSON = `
  {"name":"dealingCutoff","type":"uint64"},`
	projectConfigV4PurchaseTimeoutJSON = `
  {"name":"purchaseTimeout","type":"uint64"},`
	projectConfigRolesJSON = `
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
	projectConfigHeadJSON + projectConfigRedemptionTimeoutJSON + projectConfigRolesJSON + projectConfigTailJSON + `]}`

const projectConfigV3TupleJSON = `{"name":"config","type":"tuple","components":[` +
	projectConfigHeadJSON + projectConfigV3PricingJSON + projectConfigRedemptionTimeoutJSON + projectConfigRolesJSON +
	projectConfigV3IncomeJSON + projectConfigTailJSON + `]}`

const projectConfigTupleJSON = `{"name":"config","type":"tuple","components":[` +
	projectConfigHeadJSON + projectConfigV3PricingJSON + projectConfigV4DealingCutoffJSON +
	projectConfigRedemptionTimeoutJSON + projectConfigV4PurchaseTimeoutJSON + projectConfigRolesJSON +
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
  {"type":"function","name":"strategyDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"marketDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"supplyControllerDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"redemptionEscrowDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"incomeDistributorDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"event","name":"ProjectDeployed","anonymous":false,"inputs":[` + projectDeployedHeadJSON + `,
    {"name":"incomeDistributor","type":"address","indexed":false},
    {"name":"version","type":"string","indexed":false}]}
]`

// factoryV3ABIJSON preserves the rwa-v3 deploy encoding; its ProjectDeployed event is the
// rwa-v4 one, which keeps the same topic0. cmd/verifyabi checks it against the compiled ABI in
// internal/bindings/testdata/rwa-v3.
const factoryV3ABIJSON = `[
  {"type":"function","name":"deploy","stateMutability":"nonpayable",
    "inputs":[` + projectConfigV3TupleJSON + `],"outputs":[` + deploymentTupleJSON + `]},
  {"type":"function","name":"version","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"string"}]}
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
// ABI encodes rwa-v4; ABIV3 preserves the rwa-v3 deploy call and ABIV2 the rwa-v2 deploy call
// and ProjectDeployed event.
type Factory struct{ ABI, ABIV3, ABIV2 abi.ABI }

// NewFactory parses the rwa-v4, rwa-v3 and rwa-v2 RWAFactory ABIs
func NewFactory() Factory {
	return Factory{ABI: mustABI(factoryABIJSON), ABIV3: mustABI(factoryV3ABIJSON), ABIV2: mustABI(factoryV2ABIJSON)}
}

// ProjectConfig mirrors the rwa-v4 IRWAFactory.ProjectConfig
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
	DealingCutoff                uint64         `abi:"dealingCutoff"`
	RedemptionTimeout            uint64         `abi:"redemptionTimeout"`
	PurchaseTimeout              uint64         `abi:"purchaseTimeout"`
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

// Deployment mirrors the IRWAFactory.Deployment, which rwa-v3 and rwa-v4 share
type Deployment struct {
	Token             common.Address `abi:"token"`
	Compliance        common.Address `abi:"compliance"`
	SupplyController  common.Address `abi:"supplyController"`
	Vault             common.Address `abi:"vault"`
	RedemptionEscrow  common.Address `abi:"redemptionEscrow"`
	Strategy          common.Address `abi:"strategy"`
	IncomeDistributor common.Address `abi:"incomeDistributor"`
}

// projectConfigV3 is rwa-v3's ProjectConfig, which lacks the rwa-v4 dealing cut-off and
// purchase timeout, for packing and decoding rwa-v3 calldata
type projectConfigV3 struct {
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
	AdminTransferDelay           *big.Int       `abi:"adminTransferDelay"`
}

// projectConfigV2 is rwa-v2's ProjectConfig, which lacks the rwa-v3 pricing and income
// fields and the rwa-v4 timing fields, for packing and decoding rwa-v2 calldata
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

// PackDeploy builds calldata for the rwa-v4 deploy(ProjectConfig)
func (f Factory) PackDeploy(cfg ProjectConfig) ([]byte, error) {
	return f.ABI.Pack("deploy", cfg)
}

// PackDeployV3 builds rwa-v3 fixture calldata, omitting cfg's rwa-v4 fields. The explicit
// copy avoids go-ethereum rejecting abi tags absent from the tuple.
func (f Factory) PackDeployV3(cfg ProjectConfig) ([]byte, error) {
	return f.ABIV3.Pack("deploy", toProjectConfigV3(cfg))
}

// PackDeployV2 builds rwa-v2 fixture calldata, omitting cfg's rwa-v3 and rwa-v4 fields. The
// explicit copy avoids go-ethereum rejecting abi tags absent from the tuple.
func (f Factory) PackDeployV2(cfg ProjectConfig) ([]byte, error) {
	return f.ABIV2.Pack("deploy", projectConfigV2{
		Name:                         cfg.Name,
		Symbol:                       cfg.Symbol,
		Decimals:                     cfg.Decimals,
		ProfileDigest:                cfg.ProfileDigest,
		ProjectID:                    cfg.ProjectID,
		QuoteToken:                   cfg.QuoteToken,
		PurchasePricePerWholeToken:   cfg.PurchasePricePerWholeToken,
		RedemptionPricePerWholeToken: cfg.RedemptionPricePerWholeToken,
		RedemptionTimeout:            cfg.RedemptionTimeout,
		Admin:                        cfg.Admin,
		Auditor:                      cfg.Auditor,
		ComplianceOperator:           cfg.ComplianceOperator,
		Pricer:                       cfg.Pricer,
		Treasurer:                    cfg.Treasurer,
		RedemptionManager:            cfg.RedemptionManager,
		Treasury:                     cfg.Treasury,
		AdminTransferDelay:           cfg.AdminTransferDelay,
	})
}

func toProjectConfigV3(cfg ProjectConfig) projectConfigV3 {
	return projectConfigV3{
		Name:                         cfg.Name,
		Symbol:                       cfg.Symbol,
		Decimals:                     cfg.Decimals,
		ProfileDigest:                cfg.ProfileDigest,
		ProjectID:                    cfg.ProjectID,
		QuoteToken:                   cfg.QuoteToken,
		PurchasePricePerWholeToken:   cfg.PurchasePricePerWholeToken,
		RedemptionPricePerWholeToken: cfg.RedemptionPricePerWholeToken,
		MinPurchaseQuote:             cfg.MinPurchaseQuote,
		StrategyKind:                 cfg.StrategyKind,
		PriceFeed:                    cfg.PriceFeed,
		PriceMaxAge:                  cfg.PriceMaxAge,
		PurchaseSpreadBps:            cfg.PurchaseSpreadBps,
		RedemptionSpreadBps:          cfg.RedemptionSpreadBps,
		SequencerUptimeFeed:          cfg.SequencerUptimeFeed,
		SequencerGracePeriod:         cfg.SequencerGracePeriod,
		RedemptionTimeout:            cfg.RedemptionTimeout,
		Admin:                        cfg.Admin,
		Auditor:                      cfg.Auditor,
		ComplianceOperator:           cfg.ComplianceOperator,
		Pricer:                       cfg.Pricer,
		Treasurer:                    cfg.Treasurer,
		RedemptionManager:            cfg.RedemptionManager,
		IncomeManager:                cfg.IncomeManager,
		Treasury:                     cfg.Treasury,
		AdminTransferDelay:           cfg.AdminTransferDelay,
	}
}

// DeployCall pairs decoded ProjectConfig with its selector's factory generation.
// rwa-v2 leaves v3-only fields zero: Fixed strategy and no minimum purchase. rwa-v2 and rwa-v3
// leave the rwa-v4 DealingCutoff and PurchaseTimeout zero.
type DeployCall struct {
	ProjectConfig
	// Version is FactoryVersionV2, FactoryVersionV3 or FactoryVersionV4
	Version string
}

// UnpackDeployInput decodes the rwa-v4, rwa-v3 or rwa-v2 ProjectConfig by its four-byte
// selector, rejecting wrapped and unrelated calls. The server verifies signed roles, treasury,
// auditor and prices from calldata because ProjectDeployed omits them.
func (f Factory) UnpackDeployInput(calldata []byte) (DeployCall, error) {
	if len(calldata) < 4 {
		return DeployCall{}, fmt.Errorf("bindings: deploy calldata is %d bytes, too short for a selector", len(calldata))
	}
	v4, v3, v2 := f.ABI.Methods["deploy"], f.ABIV3.Methods["deploy"], f.ABIV2.Methods["deploy"]
	unpack := func(m abi.Method) (any, error) {
		args, err := m.Inputs.Unpack(calldata[4:])
		if err != nil {
			return nil, err
		}
		if len(args) == 0 {
			return nil, fmt.Errorf("bindings: deploy calldata decoded to no arguments")
		}
		return args[0], nil
	}
	// abi.ConvertType copies struct fields by position, so each shorter tuple cannot convert
	// into ProjectConfig directly
	switch {
	case bytes.Equal(calldata[:4], v4.ID):
		arg, err := unpack(v4)
		if err != nil {
			return DeployCall{}, err
		}
		cfg := abi.ConvertType(arg, new(ProjectConfig)).(*ProjectConfig)
		return DeployCall{ProjectConfig: *cfg, Version: FactoryVersionV4}, nil
	case bytes.Equal(calldata[:4], v3.ID):
		arg, err := unpack(v3)
		if err != nil {
			return DeployCall{}, err
		}
		old := abi.ConvertType(arg, new(projectConfigV3)).(*projectConfigV3)
		return DeployCall{
			ProjectConfig: ProjectConfig{
				Name:                         old.Name,
				Symbol:                       old.Symbol,
				Decimals:                     old.Decimals,
				ProfileDigest:                old.ProfileDigest,
				ProjectID:                    old.ProjectID,
				QuoteToken:                   old.QuoteToken,
				PurchasePricePerWholeToken:   old.PurchasePricePerWholeToken,
				RedemptionPricePerWholeToken: old.RedemptionPricePerWholeToken,
				MinPurchaseQuote:             old.MinPurchaseQuote,
				StrategyKind:                 old.StrategyKind,
				PriceFeed:                    old.PriceFeed,
				PriceMaxAge:                  old.PriceMaxAge,
				PurchaseSpreadBps:            old.PurchaseSpreadBps,
				RedemptionSpreadBps:          old.RedemptionSpreadBps,
				SequencerUptimeFeed:          old.SequencerUptimeFeed,
				SequencerGracePeriod:         old.SequencerGracePeriod,
				RedemptionTimeout:            old.RedemptionTimeout,
				Admin:                        old.Admin,
				Auditor:                      old.Auditor,
				ComplianceOperator:           old.ComplianceOperator,
				Pricer:                       old.Pricer,
				Treasurer:                    old.Treasurer,
				RedemptionManager:            old.RedemptionManager,
				IncomeManager:                old.IncomeManager,
				Treasury:                     old.Treasury,
				AdminTransferDelay:           old.AdminTransferDelay,
			},
			Version: FactoryVersionV3,
		}, nil
	case bytes.Equal(calldata[:4], v2.ID):
		arg, err := unpack(v2)
		if err != nil {
			return DeployCall{}, err
		}
		old := abi.ConvertType(arg, new(projectConfigV2)).(*projectConfigV2)
		return DeployCall{
			ProjectConfig: ProjectConfig{
				Name:                         old.Name,
				Symbol:                       old.Symbol,
				Decimals:                     old.Decimals,
				ProfileDigest:                old.ProfileDigest,
				ProjectID:                    old.ProjectID,
				QuoteToken:                   old.QuoteToken,
				PurchasePricePerWholeToken:   old.PurchasePricePerWholeToken,
				RedemptionPricePerWholeToken: old.RedemptionPricePerWholeToken,
				RedemptionTimeout:            old.RedemptionTimeout,
				Admin:                        old.Admin,
				Auditor:                      old.Auditor,
				ComplianceOperator:           old.ComplianceOperator,
				Pricer:                       old.Pricer,
				Treasurer:                    old.Treasurer,
				RedemptionManager:            old.RedemptionManager,
				Treasury:                     old.Treasury,
				AdminTransferDelay:           old.AdminTransferDelay,
			},
			Version: FactoryVersionV2,
		}, nil
	default:
		return DeployCall{}, fmt.Errorf("bindings: calldata selector 0x%x is not RWAFactory.deploy (rwa-v4 0x%x, rwa-v3 0x%x, rwa-v2 0x%x)", calldata[:4], v4.ID, v3.ID, v2.ID)
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

// ProjectDeployedEvent mirrors the ProjectDeployed event of any factory generation.
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
	// Version is the event's own version string; Layout is the event layout
	// (FactoryVersionV2 or FactoryVersionV3) whose topic0 and field list the log used.
	// rwa-v4 shares the rwa-v3 layout, see ProjectDeployedLayout.
	Version string
	Layout  string
}

// IsProjectDeployed reports whether topic0 is the rwa-v4/rwa-v3 or rwa-v2 ProjectDeployed event
func (f Factory) IsProjectDeployed(topic0 common.Hash) bool {
	return topic0 == f.ABI.Events["ProjectDeployed"].ID || topic0 == f.ABIV2.Events["ProjectDeployed"].ID
}

// UnpackProjectDeployed decodes a ProjectDeployed log, choosing the rwa-v3 or rwa-v2
// layout by topic0 and records it in Layout; callers compare Layout with
// ProjectDeployedLayout(Version)
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
		Token:             partial.Token,
		Compliance:        partial.Compliance,
		SupplyController:  partial.SupplyController,
		Vault:             partial.Vault,
		RedemptionEscrow:  partial.RedemptionEscrow,
		Strategy:          partial.Strategy,
		IncomeDistributor: partial.IncomeDistributor,
		Version:           partial.Version,
		Layout:            want,
	}
	if len(topics) >= 3 {
		ev.ProjectID = topics[1]
		ev.ProfileDigest = topics[2]
	}
	return ev, nil
}

// EventID returns the rwa-v4 keccak256 topic0 for the named event
func (f Factory) EventID(name string) common.Hash { return f.ABI.Events[name].ID }
