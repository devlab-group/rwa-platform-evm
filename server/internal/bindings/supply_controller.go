package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const mintAttestationTupleJSON = `{"name":"attestation","type":"tuple","components":[
  {"name":"auditor","type":"address"},
  {"name":"profileDigest","type":"bytes32"},
  {"name":"recordKey","type":"bytes32"},
  {"name":"metadataDigest","type":"bytes32"},
  {"name":"amount","type":"uint256"},
  {"name":"nonce","type":"uint256"},
  {"name":"validUntil","type":"uint64"},
  {"name":"vault","type":"address"}]}`

const burnAttestationTupleJSON = `{"name":"attestation","type":"tuple","components":[
  {"name":"auditor","type":"address"},
  {"name":"profileDigest","type":"bytes32"},
  {"name":"operationId","type":"bytes32"},
  {"name":"metadataDigest","type":"bytes32"},
  {"name":"amount","type":"uint256"},
  {"name":"nonce","type":"uint256"},
  {"name":"validUntil","type":"uint64"},
  {"name":"vault","type":"address"}]}`

const supplyControllerABIJSON = `[
  {"type":"function","name":"mint","stateMutability":"nonpayable",
    "inputs":[` + mintAttestationTupleJSON + `,{"name":"signature","type":"bytes"}],
    "outputs":[]},
  {"type":"function","name":"burn","stateMutability":"nonpayable",
    "inputs":[` + burnAttestationTupleJSON + `,{"name":"signature","type":"bytes"}],
    "outputs":[]},
  {"type":"function","name":"setAuditor","stateMutability":"nonpayable",
    "inputs":[{"name":"newAuditor","type":"address"}],"outputs":[]},
  {"type":"function","name":"auditor","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"profileDigest","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"bytes32"}]},
  {"type":"function","name":"vault","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"token","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"nonceUsed","stateMutability":"view","inputs":[{"name":"nonce","type":"uint256"}],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"recordKeyUsed","stateMutability":"view","inputs":[{"name":"recordKey","type":"bytes32"}],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"operationIdUsed","stateMutability":"view","inputs":[{"name":"operationId","type":"bytes32"}],"outputs":[{"name":"","type":"bool"}]},
  {"type":"event","name":"Minted","anonymous":false,"inputs":[
    {"name":"recordKey","type":"bytes32","indexed":true},
    {"name":"metadataDigest","type":"bytes32","indexed":true},
    {"name":"vault","type":"address","indexed":true},
    {"name":"amount","type":"uint256","indexed":false},
    {"name":"nonce","type":"uint256","indexed":false},
    {"name":"auditor","type":"address","indexed":false}]},
  {"type":"event","name":"Burned","anonymous":false,"inputs":[
    {"name":"operationId","type":"bytes32","indexed":true},
    {"name":"metadataDigest","type":"bytes32","indexed":true},
    {"name":"vault","type":"address","indexed":true},
    {"name":"amount","type":"uint256","indexed":false},
    {"name":"nonce","type":"uint256","indexed":false},
    {"name":"auditor","type":"address","indexed":false}]},
  {"type":"event","name":"AuditorChanged","anonymous":false,"inputs":[
    {"name":"previousAuditor","type":"address","indexed":true},
    {"name":"newAuditor","type":"address","indexed":true},
    {"name":"caller","type":"address","indexed":true}]}
]`

// SupplyController encodes calldata / decodes events for ISupplyController.
type SupplyController struct{ ABI abi.ABI }

// NewSupplyController parses the SupplyController ABI once.
func NewSupplyController() SupplyController {
	return SupplyController{ABI: mustABI(supplyControllerABIJSON)}
}

// MintAttestation mirrors ISupplyController.MintAttestation. Field names use
// `abi:` tags so go-ethereum's tuple packer matches them to the Solidity
// component names regardless of Go field order.
type MintAttestation struct {
	Auditor        common.Address `abi:"auditor"`
	ProfileDigest  [32]byte       `abi:"profileDigest"`
	RecordKey      [32]byte       `abi:"recordKey"`
	MetadataDigest [32]byte       `abi:"metadataDigest"`
	Amount         *big.Int       `abi:"amount"`
	Nonce          *big.Int       `abi:"nonce"`
	ValidUntil     uint64         `abi:"validUntil"`
	Vault          common.Address `abi:"vault"`
}

// BurnAttestation mirrors ISupplyController.BurnAttestation.
type BurnAttestation struct {
	Auditor        common.Address `abi:"auditor"`
	ProfileDigest  [32]byte       `abi:"profileDigest"`
	OperationID    [32]byte       `abi:"operationId"`
	MetadataDigest [32]byte       `abi:"metadataDigest"`
	Amount         *big.Int       `abi:"amount"`
	Nonce          *big.Int       `abi:"nonce"`
	ValidUntil     uint64         `abi:"validUntil"`
	Vault          common.Address `abi:"vault"`
}

// PackMint builds calldata for mint(MintAttestation,bytes).
func (s SupplyController) PackMint(a MintAttestation, signature []byte) ([]byte, error) {
	return s.ABI.Pack("mint", a, signature)
}

// PackBurn builds calldata for burn(BurnAttestation,bytes).
func (s SupplyController) PackBurn(b BurnAttestation, signature []byte) ([]byte, error) {
	return s.ABI.Pack("burn", b, signature)
}

// PackSetAuditor builds calldata for setAuditor(address).
func (s SupplyController) PackSetAuditor(newAuditor common.Address) ([]byte, error) {
	return s.ABI.Pack("setAuditor", newAuditor)
}

// MintedEvent mirrors the Minted event's non-indexed data.
type MintedEvent struct {
	RecordKey      common.Hash
	MetadataDigest common.Hash
	Vault          common.Address
	Amount         *big.Int
	Nonce          *big.Int
	Auditor        common.Address
}

// UnpackMinted decodes a Minted log.
func (s SupplyController) UnpackMinted(data []byte, topics []common.Hash) (MintedEvent, error) {
	var partial struct {
		Amount  *big.Int
		Nonce   *big.Int
		Auditor common.Address
	}
	if err := s.ABI.UnpackIntoInterface(&partial, "Minted", data); err != nil {
		return MintedEvent{}, err
	}
	ev := MintedEvent{Amount: partial.Amount, Nonce: partial.Nonce, Auditor: partial.Auditor}
	if len(topics) >= 4 {
		ev.RecordKey = topics[1]
		ev.MetadataDigest = topics[2]
		ev.Vault = common.HexToAddress(topics[3].Hex())
	}
	return ev, nil
}

// BurnedEvent mirrors the Burned event's non-indexed data.
type BurnedEvent struct {
	OperationID    common.Hash
	MetadataDigest common.Hash
	Vault          common.Address
	Amount         *big.Int
	Nonce          *big.Int
	Auditor        common.Address
}

// UnpackBurned decodes a Burned log.
func (s SupplyController) UnpackBurned(data []byte, topics []common.Hash) (BurnedEvent, error) {
	var partial struct {
		Amount  *big.Int
		Nonce   *big.Int
		Auditor common.Address
	}
	if err := s.ABI.UnpackIntoInterface(&partial, "Burned", data); err != nil {
		return BurnedEvent{}, err
	}
	ev := BurnedEvent{Amount: partial.Amount, Nonce: partial.Nonce, Auditor: partial.Auditor}
	if len(topics) >= 4 {
		ev.OperationID = topics[1]
		ev.MetadataDigest = topics[2]
		ev.Vault = common.HexToAddress(topics[3].Hex())
	}
	return ev, nil
}

// EventID returns the keccak256 topic0 for the named event.
func (s SupplyController) EventID(name string) common.Hash {
	return s.ABI.Events[name].ID
}
