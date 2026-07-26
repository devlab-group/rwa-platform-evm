package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const redemptionEscrowABIJSON = `[
  {"type":"function","name":"requestRedemption","stateMutability":"nonpayable","inputs":[
    {"name":"rwaAmount","type":"uint256"},{"name":"minQuoteOut","type":"uint256"},{"name":"deadline","type":"uint64"}],
    "outputs":[{"name":"id","type":"uint256"}]},
  {"type":"function","name":"fundRedemption","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"claimRedemption","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"rejectRedemption","stateMutability":"nonpayable","inputs":[
    {"name":"id","type":"uint256"},{"name":"reasonCode","type":"bytes32"}],"outputs":[]},
  {"type":"function","name":"cancelRedemption","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"getRedemption","stateMutability":"view","inputs":[{"name":"id","type":"uint256"}],
    "outputs":[{"name":"","type":"tuple","components":[
      {"name":"beneficiary","type":"address"},{"name":"rwaAmount","type":"uint256"},
      {"name":"quoteAmount","type":"uint256"},{"name":"createdAt","type":"uint64"},{"name":"status","type":"uint8"}]}]},
  {"type":"function","name":"previewRedeem","stateMutability":"view","inputs":[{"name":"rwaAmount","type":"uint256"}],"outputs":[{"name":"quoteAmount","type":"uint256"}]},
  {"type":"function","name":"redemptionTimeout","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint64"}]},
  {"type":"function","name":"nextId","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"token","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"quoteToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"vault","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"strategy","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"event","name":"RedemptionRequested","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"beneficiary","type":"address","indexed":true},
    {"name":"rwaAmount","type":"uint256","indexed":false},{"name":"quoteAmount","type":"uint256","indexed":false},
    {"name":"createdAt","type":"uint64","indexed":false}]},
  {"type":"event","name":"RedemptionFunded","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"funder","type":"address","indexed":true},
    {"name":"quoteAmount","type":"uint256","indexed":false}]},
  {"type":"event","name":"RedemptionCompleted","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"beneficiary","type":"address","indexed":true},
    {"name":"rwaAmount","type":"uint256","indexed":false},{"name":"quoteAmount","type":"uint256","indexed":false}]},
  {"type":"event","name":"RedemptionRejected","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"reasonCode","type":"bytes32","indexed":true},
    {"name":"caller","type":"address","indexed":true}]},
  {"type":"event","name":"RedemptionCancelled","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"beneficiary","type":"address","indexed":true}]}
]`

// RedemptionEscrow encodes calldata / decodes events for IRedemptionEscrow.
type RedemptionEscrow struct{ ABI abi.ABI }

// NewRedemptionEscrow parses the RedemptionEscrow ABI once.
func NewRedemptionEscrow() RedemptionEscrow {
	return RedemptionEscrow{ABI: mustABI(redemptionEscrowABIJSON)}
}

// RedemptionRequestTuple mirrors IRedemptionEscrow.RedemptionRequest.
type RedemptionRequestTuple struct {
	Beneficiary common.Address `abi:"beneficiary"`
	RWAAmount   *big.Int       `abi:"rwaAmount"`
	QuoteAmount *big.Int       `abi:"quoteAmount"`
	CreatedAt   uint64         `abi:"createdAt"`
	Status      uint8          `abi:"status"`
}

// PackRequestRedemption builds calldata for requestRedemption(uint256,uint256,uint64).
func (r RedemptionEscrow) PackRequestRedemption(rwaAmount, minQuoteOut *big.Int, deadline uint64) ([]byte, error) {
	return r.ABI.Pack("requestRedemption", rwaAmount, minQuoteOut, deadline)
}

// PackClaimRedemption builds calldata for claimRedemption(uint256).
func (r RedemptionEscrow) PackClaimRedemption(id *big.Int) ([]byte, error) {
	return r.ABI.Pack("claimRedemption", id)
}

// PackCancelRedemption builds calldata for cancelRedemption(uint256).
func (r RedemptionEscrow) PackCancelRedemption(id *big.Int) ([]byte, error) {
	return r.ABI.Pack("cancelRedemption", id)
}

// PackGetRedemption builds calldata for the getRedemption(uint256) view call.
func (r RedemptionEscrow) PackGetRedemption(id *big.Int) ([]byte, error) {
	return r.ABI.Pack("getRedemption", id)
}

// UnpackGetRedemption decodes the return value of getRedemption.
func (r RedemptionEscrow) UnpackGetRedemption(data []byte) (RedemptionRequestTuple, error) {
	out, err := r.ABI.Unpack("getRedemption", data)
	if err != nil {
		return RedemptionRequestTuple{}, err
	}
	rec := abi.ConvertType(out[0], new(RedemptionRequestTuple)).(*RedemptionRequestTuple)
	return *rec, nil
}

// PackPreviewRedeem builds calldata for previewRedeem(uint256).
func (r RedemptionEscrow) PackPreviewRedeem(rwaAmount *big.Int) ([]byte, error) {
	return r.ABI.Pack("previewRedeem", rwaAmount)
}

// UnpackPreviewRedeem decodes the return value of previewRedeem.
func (r RedemptionEscrow) UnpackPreviewRedeem(data []byte) (*big.Int, error) {
	out, err := r.ABI.Unpack("previewRedeem", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// RedemptionRequestedEvent mirrors the RedemptionRequested event.
type RedemptionRequestedEvent struct {
	ID          *big.Int
	Beneficiary common.Address
	RWAAmount   *big.Int
	QuoteAmount *big.Int
	CreatedAt   uint64
}

// UnpackRedemptionRequested decodes a RedemptionRequested log.
func (r RedemptionEscrow) UnpackRedemptionRequested(data []byte, topics []common.Hash) (RedemptionRequestedEvent, error) {
	// Explicit abi tags are required for RWAAmount: go-ethereum's untagged
	// struct-field matching maps the ABI name "rwaAmount" to the Go field
	// name ToCamelCase("rwaAmount") == "RwaAmount" (only the leading
	// character is upcased), which never matches the "RWA" all-caps
	// acronym spelling this codebase otherwise uses everywhere else.
	var partial struct {
		RWAAmount   *big.Int `abi:"rwaAmount"`
		QuoteAmount *big.Int `abi:"quoteAmount"`
		CreatedAt   uint64   `abi:"createdAt"`
	}
	if err := r.ABI.UnpackIntoInterface(&partial, "RedemptionRequested", data); err != nil {
		return RedemptionRequestedEvent{}, err
	}
	ev := RedemptionRequestedEvent{RWAAmount: partial.RWAAmount, QuoteAmount: partial.QuoteAmount, CreatedAt: partial.CreatedAt}
	if len(topics) >= 3 {
		ev.ID = topics[1].Big()
		ev.Beneficiary = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// RedemptionFundedEvent mirrors the RedemptionFunded event.
type RedemptionFundedEvent struct {
	ID          *big.Int
	Funder      common.Address
	QuoteAmount *big.Int
}

// UnpackRedemptionFunded decodes a RedemptionFunded log.
func (r RedemptionEscrow) UnpackRedemptionFunded(data []byte, topics []common.Hash) (RedemptionFundedEvent, error) {
	var partial struct{ QuoteAmount *big.Int }
	if err := r.ABI.UnpackIntoInterface(&partial, "RedemptionFunded", data); err != nil {
		return RedemptionFundedEvent{}, err
	}
	ev := RedemptionFundedEvent{QuoteAmount: partial.QuoteAmount}
	if len(topics) >= 3 {
		ev.ID = topics[1].Big()
		ev.Funder = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// RedemptionCompletedEvent mirrors the RedemptionCompleted event.
type RedemptionCompletedEvent struct {
	ID          *big.Int
	Beneficiary common.Address
	RWAAmount   *big.Int
	QuoteAmount *big.Int
}

// UnpackRedemptionCompleted decodes a RedemptionCompleted log.
func (r RedemptionEscrow) UnpackRedemptionCompleted(data []byte, topics []common.Hash) (RedemptionCompletedEvent, error) {
	// See UnpackRedemptionRequested above for why RWAAmount needs an
	// explicit abi tag.
	var partial struct {
		RWAAmount   *big.Int `abi:"rwaAmount"`
		QuoteAmount *big.Int `abi:"quoteAmount"`
	}
	if err := r.ABI.UnpackIntoInterface(&partial, "RedemptionCompleted", data); err != nil {
		return RedemptionCompletedEvent{}, err
	}
	ev := RedemptionCompletedEvent{RWAAmount: partial.RWAAmount, QuoteAmount: partial.QuoteAmount}
	if len(topics) >= 3 {
		ev.ID = topics[1].Big()
		ev.Beneficiary = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// RedemptionRejectedEvent mirrors the RedemptionRejected event (no non-indexed data).
type RedemptionRejectedEvent struct {
	ID         *big.Int
	ReasonCode common.Hash
	Caller     common.Address
}

// UnpackRedemptionRejected decodes a RedemptionRejected log (purely indexed).
func (r RedemptionEscrow) UnpackRedemptionRejected(topics []common.Hash) RedemptionRejectedEvent {
	var ev RedemptionRejectedEvent
	if len(topics) >= 4 {
		ev.ID = topics[1].Big()
		ev.ReasonCode = topics[2]
		ev.Caller = common.HexToAddress(topics[3].Hex())
	}
	return ev
}

// RedemptionCancelledEvent mirrors the RedemptionCancelled event (no non-indexed data).
type RedemptionCancelledEvent struct {
	ID          *big.Int
	Beneficiary common.Address
}

// UnpackRedemptionCancelled decodes a RedemptionCancelled log (purely indexed).
func (r RedemptionEscrow) UnpackRedemptionCancelled(topics []common.Hash) RedemptionCancelledEvent {
	var ev RedemptionCancelledEvent
	if len(topics) >= 3 {
		ev.ID = topics[1].Big()
		ev.Beneficiary = common.HexToAddress(topics[2].Hex())
	}
	return ev
}

// EventID returns the keccak256 topic0 for the named event.
func (r RedemptionEscrow) EventID(name string) common.Hash { return r.ABI.Events[name].ID }
