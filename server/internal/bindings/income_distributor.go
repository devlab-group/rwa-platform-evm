package bindings

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const incomeDistributorABIJSON = `[
  {"type":"function","name":"createDistribution","stateMutability":"nonpayable","inputs":[
    {"name":"merkleRoot","type":"bytes32"},{"name":"total","type":"uint256"},{"name":"recordBlock","type":"uint64"},
    {"name":"claimDeadline","type":"uint64"},{"name":"snapshotURI","type":"string"},{"name":"snapshotSha256","type":"bytes32"}],
    "outputs":[{"name":"id","type":"uint256"}]},
  {"type":"function","name":"claim","stateMutability":"nonpayable","inputs":[
    {"name":"id","type":"uint256"},{"name":"amount","type":"uint256"},{"name":"proof","type":"bytes32[]"}],"outputs":[]},
  {"type":"function","name":"reclaimUnclaimed","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"extendClaimDeadline","stateMutability":"nonpayable","inputs":[
    {"name":"id","type":"uint256"},{"name":"newDeadline","type":"uint64"}],"outputs":[]},
  {"type":"function","name":"isClaimed","stateMutability":"view","inputs":[
    {"name":"id","type":"uint256"},{"name":"account","type":"address"}],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"nextId","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"token","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"quoteToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"vault","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"event","name":"DistributionCreated","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"merkleRoot","type":"bytes32","indexed":false},
    {"name":"total","type":"uint256","indexed":false},{"name":"recordBlock","type":"uint64","indexed":false},
    {"name":"claimDeadline","type":"uint64","indexed":false},{"name":"snapshotURI","type":"string","indexed":false},
    {"name":"snapshotSha256","type":"bytes32","indexed":false},{"name":"caller","type":"address","indexed":true}]},
  {"type":"event","name":"IncomeClaimed","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"account","type":"address","indexed":true},
    {"name":"amount","type":"uint256","indexed":false}]},
  {"type":"event","name":"UnclaimedReclaimed","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"treasury","type":"address","indexed":true},
    {"name":"amount","type":"uint256","indexed":false}]},
  {"type":"event","name":"ClaimDeadlineExtended","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"previousDeadline","type":"uint64","indexed":false},
    {"name":"newDeadline","type":"uint64","indexed":false},{"name":"caller","type":"address","indexed":true}]}
]`

// IncomeDistributor encodes calldata for, and decodes events and views of, the rwa-v3
// IIncomeDistributor
type IncomeDistributor struct{ ABI abi.ABI }

// NewIncomeDistributor parses the IncomeDistributor ABI
func NewIncomeDistributor() IncomeDistributor {
	return IncomeDistributor{ABI: mustABI(incomeDistributorABIJSON)}
}

// EventID returns the keccak256 topic0 for the named event
func (d IncomeDistributor) EventID(name string) common.Hash { return d.ABI.Events[name].ID }

// DistributionCreatedEvent mirrors DistributionCreated: id and caller are indexed
type DistributionCreatedEvent struct {
	ID             *big.Int
	MerkleRoot     common.Hash
	Total          *big.Int
	RecordBlock    uint64
	ClaimDeadline  uint64
	SnapshotURI    string
	SnapshotSha256 common.Hash
	Caller         common.Address
}

// UnpackDistributionCreated decodes a DistributionCreated log
func (d IncomeDistributor) UnpackDistributionCreated(data []byte, topics []common.Hash) (DistributionCreatedEvent, error) {
	var partial struct {
		MerkleRoot     [32]byte
		Total          *big.Int
		RecordBlock    uint64
		ClaimDeadline  uint64
		SnapshotURI    string
		SnapshotSha256 [32]byte
	}
	if err := d.ABI.UnpackIntoInterface(&partial, "DistributionCreated", data); err != nil {
		return DistributionCreatedEvent{}, err
	}
	if len(topics) != 3 {
		return DistributionCreatedEvent{}, fmt.Errorf("bindings: DistributionCreated log has %d topics, want 3", len(topics))
	}
	return DistributionCreatedEvent{
		ID: new(big.Int).SetBytes(topics[1].Bytes()), MerkleRoot: partial.MerkleRoot, Total: partial.Total,
		RecordBlock: partial.RecordBlock, ClaimDeadline: partial.ClaimDeadline, SnapshotURI: partial.SnapshotURI,
		SnapshotSha256: partial.SnapshotSha256, Caller: common.HexToAddress(topics[2].Hex()),
	}, nil
}

// IncomeClaimedEvent mirrors IncomeClaimed: id and account are indexed
type IncomeClaimedEvent struct {
	ID      *big.Int
	Account common.Address
	Amount  *big.Int
}

// UnpackIncomeClaimed decodes an IncomeClaimed log
func (d IncomeDistributor) UnpackIncomeClaimed(data []byte, topics []common.Hash) (IncomeClaimedEvent, error) {
	var partial struct{ Amount *big.Int }
	if err := d.ABI.UnpackIntoInterface(&partial, "IncomeClaimed", data); err != nil {
		return IncomeClaimedEvent{}, err
	}
	if len(topics) != 3 {
		return IncomeClaimedEvent{}, fmt.Errorf("bindings: IncomeClaimed log has %d topics, want 3", len(topics))
	}
	return IncomeClaimedEvent{
		ID: new(big.Int).SetBytes(topics[1].Bytes()), Account: common.HexToAddress(topics[2].Hex()), Amount: partial.Amount,
	}, nil
}

// UnclaimedReclaimedEvent mirrors UnclaimedReclaimed: id and treasury are indexed
type UnclaimedReclaimedEvent struct {
	ID       *big.Int
	Treasury common.Address
	Amount   *big.Int
}

// UnpackUnclaimedReclaimed decodes an UnclaimedReclaimed log
func (d IncomeDistributor) UnpackUnclaimedReclaimed(data []byte, topics []common.Hash) (UnclaimedReclaimedEvent, error) {
	var partial struct{ Amount *big.Int }
	if err := d.ABI.UnpackIntoInterface(&partial, "UnclaimedReclaimed", data); err != nil {
		return UnclaimedReclaimedEvent{}, err
	}
	if len(topics) != 3 {
		return UnclaimedReclaimedEvent{}, fmt.Errorf("bindings: UnclaimedReclaimed log has %d topics, want 3", len(topics))
	}
	return UnclaimedReclaimedEvent{
		ID: new(big.Int).SetBytes(topics[1].Bytes()), Treasury: common.HexToAddress(topics[2].Hex()), Amount: partial.Amount,
	}, nil
}

// ClaimDeadlineExtendedEvent mirrors ClaimDeadlineExtended: id and caller are indexed
type ClaimDeadlineExtendedEvent struct {
	ID               *big.Int
	PreviousDeadline uint64
	NewDeadline      uint64
	Caller           common.Address
}

// UnpackClaimDeadlineExtended decodes a ClaimDeadlineExtended log
func (d IncomeDistributor) UnpackClaimDeadlineExtended(data []byte, topics []common.Hash) (ClaimDeadlineExtendedEvent, error) {
	var partial struct {
		PreviousDeadline uint64
		NewDeadline      uint64
	}
	if err := d.ABI.UnpackIntoInterface(&partial, "ClaimDeadlineExtended", data); err != nil {
		return ClaimDeadlineExtendedEvent{}, err
	}
	if len(topics) != 3 {
		return ClaimDeadlineExtendedEvent{}, fmt.Errorf("bindings: ClaimDeadlineExtended log has %d topics, want 3", len(topics))
	}
	return ClaimDeadlineExtendedEvent{
		ID: new(big.Int).SetBytes(topics[1].Bytes()), PreviousDeadline: partial.PreviousDeadline,
		NewDeadline: partial.NewDeadline, Caller: common.HexToAddress(topics[2].Hex()),
	}, nil
}
