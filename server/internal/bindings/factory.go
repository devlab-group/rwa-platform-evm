package bindings

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const projectConfigTupleJSON = `{"name":"config","type":"tuple","components":[
  {"name":"name","type":"string"},
  {"name":"symbol","type":"string"},
  {"name":"decimals","type":"uint8"},
  {"name":"profileDigest","type":"bytes32"},
  {"name":"projectId","type":"bytes32"},
  {"name":"quoteToken","type":"address"},
  {"name":"purchasePricePerWholeToken","type":"uint256"},
  {"name":"redemptionPricePerWholeToken","type":"uint256"},
  {"name":"redemptionTimeout","type":"uint64"},
  {"name":"admin","type":"address"},
  {"name":"auditor","type":"address"},
  {"name":"complianceOperator","type":"address"},
  {"name":"pricer","type":"address"},
  {"name":"treasurer","type":"address"},
  {"name":"redemptionManager","type":"address"},
  {"name":"treasury","type":"address"},
  {"name":"adminTransferDelay","type":"uint48"}]}`

const deploymentTupleJSON = `{"name":"","type":"tuple","components":[
  {"name":"token","type":"address"},
  {"name":"compliance","type":"address"},
  {"name":"supplyController","type":"address"},
  {"name":"vault","type":"address"},
  {"name":"redemptionEscrow","type":"address"},
  {"name":"strategy","type":"address"}]}`

const factoryABIJSON = `[
  {"type":"function","name":"deploy","stateMutability":"nonpayable",
    "inputs":[` + projectConfigTupleJSON + `],"outputs":[` + deploymentTupleJSON + `]},
  {"type":"function","name":"version","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"string"}]},
  {"type":"function","name":"complianceTokenDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"marketDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"supplyControllerDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"redemptionEscrowDeployer","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"event","name":"ProjectDeployed","anonymous":false,"inputs":[
    {"name":"projectId","type":"bytes32","indexed":true},
    {"name":"profileDigest","type":"bytes32","indexed":true},
    {"name":"token","type":"address","indexed":false},
    {"name":"compliance","type":"address","indexed":false},
    {"name":"supplyController","type":"address","indexed":false},
    {"name":"vault","type":"address","indexed":false},
    {"name":"redemptionEscrow","type":"address","indexed":false},
    {"name":"strategy","type":"address","indexed":false},
    {"name":"version","type":"string","indexed":false}]}
]`

// Factory encodes calldata / decodes events for IRWAFactory, plus the
// concrete RWAFactory contract's public immutable getters for its 4
// deployment-helper addresses — those aren't part of the
// frozen IRWAFactory interface, so this is the concrete-contract ABI, same
// as other "concrete getter" additions elsewhere in this package.
type Factory struct{ ABI abi.ABI }

// NewFactory parses the RWAFactory ABI once.
func NewFactory() Factory { return Factory{ABI: mustABI(factoryABIJSON)} }

// ProjectConfig mirrors IRWAFactory.ProjectConfig.
type ProjectConfig struct {
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
	AdminTransferDelay           *big.Int       `abi:"adminTransferDelay"` // uint48, packed/unpacked as *big.Int by go-ethereum
}

// Deployment mirrors IRWAFactory.Deployment.
type Deployment struct {
	Token            common.Address `abi:"token"`
	Compliance       common.Address `abi:"compliance"`
	SupplyController common.Address `abi:"supplyController"`
	Vault            common.Address `abi:"vault"`
	RedemptionEscrow common.Address `abi:"redemptionEscrow"`
	Strategy         common.Address `abi:"strategy"`
}

// PackDeploy builds calldata for deploy(ProjectConfig).
func (f Factory) PackDeploy(cfg ProjectConfig) ([]byte, error) {
	return f.ABI.Pack("deploy", cfg)
}

// UnpackDeployInput decodes the ProjectConfig from a mined RWAFactory.deploy
// transaction's INPUT calldata. The server observes deployments broadcast from
// the admin's wallet (it no longer submits them itself), so the intended
// role/treasury/auditor/price configuration — none of which appears in the
// ProjectDeployed event — is recovered here from the exact calldata the admin
// signed, and cross-checked against on-chain state during verification. The
// leading 4 bytes MUST be RWAFactory.deploy's selector, so calldata for any
// other function (e.g. a deploy wrapped in an unrecognized call) is rejected
// rather than mis-decoded.
func (f Factory) UnpackDeployInput(calldata []byte) (ProjectConfig, error) {
	deploy := f.ABI.Methods["deploy"]
	if len(calldata) < 4 {
		return ProjectConfig{}, fmt.Errorf("bindings: deploy calldata is %d bytes, too short for a selector", len(calldata))
	}
	if !bytesEqual(calldata[:4], deploy.ID) {
		return ProjectConfig{}, fmt.Errorf("bindings: calldata selector 0x%x is not RWAFactory.deploy (0x%x)", calldata[:4], deploy.ID)
	}
	args, err := deploy.Inputs.Unpack(calldata[4:])
	if err != nil {
		return ProjectConfig{}, err
	}
	if len(args) == 0 {
		return ProjectConfig{}, fmt.Errorf("bindings: deploy calldata decoded to no arguments")
	}
	cfg := abi.ConvertType(args[0], new(ProjectConfig)).(*ProjectConfig)
	return *cfg, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// ProjectDeployedEvent mirrors the ProjectDeployed event.
type ProjectDeployedEvent struct {
	ProjectID        common.Hash
	ProfileDigest    common.Hash
	Token            common.Address
	Compliance       common.Address
	SupplyController common.Address
	Vault            common.Address
	RedemptionEscrow common.Address
	Strategy         common.Address
	Version          string
}

// UnpackProjectDeployed decodes a ProjectDeployed log.
func (f Factory) UnpackProjectDeployed(data []byte, topics []common.Hash) (ProjectDeployedEvent, error) {
	var partial struct {
		Token            common.Address
		Compliance       common.Address
		SupplyController common.Address
		Vault            common.Address
		RedemptionEscrow common.Address
		Strategy         common.Address
		Version          string
	}
	if err := f.ABI.UnpackIntoInterface(&partial, "ProjectDeployed", data); err != nil {
		return ProjectDeployedEvent{}, err
	}
	ev := ProjectDeployedEvent{
		Token: partial.Token, Compliance: partial.Compliance, SupplyController: partial.SupplyController,
		Vault: partial.Vault, RedemptionEscrow: partial.RedemptionEscrow, Strategy: partial.Strategy,
		Version: partial.Version,
	}
	if len(topics) >= 3 {
		ev.ProjectID = topics[1]
		ev.ProfileDigest = topics[2]
	}
	return ev, nil
}

// EventID returns the keccak256 topic0 for the named event.
func (f Factory) EventID(name string) common.Hash { return f.ABI.Events[name].ID }
