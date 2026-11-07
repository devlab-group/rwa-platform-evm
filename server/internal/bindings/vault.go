package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const vaultABIJSON = `[
  {"type":"function","name":"buy","stateMutability":"nonpayable","inputs":[
    {"name":"tokenAmount","type":"uint256"},{"name":"maxQuoteAmount","type":"uint256"},
    {"name":"recipient","type":"address"},{"name":"deadline","type":"uint64"}],"outputs":[]},
  {"type":"function","name":"withdrawProceeds","stateMutability":"nonpayable","inputs":[{"name":"amount","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"setStrategy","stateMutability":"nonpayable","inputs":[{"name":"newStrategy","type":"address"}],"outputs":[]},
  {"type":"function","name":"setTreasury","stateMutability":"nonpayable","inputs":[{"name":"newTreasury","type":"address"}],"outputs":[]},
  {"type":"function","name":"inventory","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"previewBuy","stateMutability":"view","inputs":[{"name":"tokenAmount","type":"uint256"}],"outputs":[{"name":"quoteAmount","type":"uint256"}]},
  {"type":"function","name":"token","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"quoteToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"strategy","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"treasury","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"minPurchaseQuote","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"setMinPurchaseQuote","stateMutability":"nonpayable","inputs":[{"name":"newMinimum","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"requestPurchase","stateMutability":"nonpayable","inputs":[
    {"name":"tokenAmount","type":"uint256"},{"name":"maxQuoteAmount","type":"uint256"},
    {"name":"recipient","type":"address"},{"name":"deadline","type":"uint64"}],"outputs":[{"name":"id","type":"uint256"}]},
  {"type":"function","name":"settlePurchase","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"cancelPurchase","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"rejectPurchase","stateMutability":"nonpayable","inputs":[{"name":"id","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"getPurchase","stateMutability":"view","inputs":[{"name":"id","type":"uint256"}],
    "outputs":[{"name":"","type":"tuple","components":[
      {"name":"buyer","type":"address"},{"name":"createdAt","type":"uint64"},{"name":"status","type":"uint8"},
      {"name":"recipient","type":"address"},{"name":"strategy","type":"address"},{"name":"baseRound","type":"uint80"},
      {"name":"tokenAmount","type":"uint256"},{"name":"maxQuoteAmount","type":"uint256"}]}]},
  {"type":"function","name":"nextPurchaseId","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"pendingPurchaseQuote","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"purchaseTimeout","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint64"}]},
  {"type":"event","name":"MinPurchaseQuoteChanged","anonymous":false,"inputs":[
    {"name":"previousMinimum","type":"uint256","indexed":false},{"name":"newMinimum","type":"uint256","indexed":false},
    {"name":"caller","type":"address","indexed":true}]},
  {"type":"event","name":"Purchased","anonymous":false,"inputs":[
    {"name":"buyer","type":"address","indexed":true},{"name":"recipient","type":"address","indexed":true},
    {"name":"tokenAmount","type":"uint256","indexed":false},{"name":"quoteAmount","type":"uint256","indexed":false},
    {"name":"strategy","type":"address","indexed":false}]},
  {"type":"event","name":"PurchaseRequested","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"buyer","type":"address","indexed":true},
    {"name":"recipient","type":"address","indexed":true},{"name":"tokenAmount","type":"uint256","indexed":false},
    {"name":"maxQuoteAmount","type":"uint256","indexed":false},{"name":"strategy","type":"address","indexed":false},
    {"name":"baseRound","type":"uint80","indexed":false},{"name":"createdAt","type":"uint64","indexed":false}]},
  {"type":"event","name":"PurchaseSettled","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"roundId","type":"uint80","indexed":false},
    {"name":"quoteAmount","type":"uint256","indexed":false},{"name":"refund","type":"uint256","indexed":false}]},
  {"type":"event","name":"PurchaseRefunded","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"roundId","type":"uint80","indexed":false},
    {"name":"quoteAmount","type":"uint256","indexed":false},{"name":"refund","type":"uint256","indexed":false}]},
  {"type":"event","name":"PurchaseCancelled","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"buyer","type":"address","indexed":true},
    {"name":"refund","type":"uint256","indexed":false}]},
  {"type":"event","name":"PurchaseRejected","anonymous":false,"inputs":[
    {"name":"id","type":"uint256","indexed":true},{"name":"buyer","type":"address","indexed":true},
    {"name":"refund","type":"uint256","indexed":false}]},
  {"type":"event","name":"ProceedsWithdrawn","anonymous":false,"inputs":[
    {"name":"treasury","type":"address","indexed":true},{"name":"quoteAmount","type":"uint256","indexed":false},
    {"name":"caller","type":"address","indexed":true}]},
  {"type":"event","name":"StrategyChanged","anonymous":false,"inputs":[
    {"name":"previousStrategy","type":"address","indexed":true},{"name":"newStrategy","type":"address","indexed":true},
    {"name":"caller","type":"address","indexed":true}]},
  {"type":"event","name":"TreasuryChanged","anonymous":false,"inputs":[
    {"name":"previousTreasury","type":"address","indexed":true},{"name":"newTreasury","type":"address","indexed":true},
    {"name":"caller","type":"address","indexed":true}]}
]`

// Vault encodes calldata / decodes events for IVault.
type Vault struct{ ABI abi.ABI }

// NewVault parses the Vault ABI
func NewVault() Vault { return Vault{ABI: mustABI(vaultABIJSON)} }

// PackBuy builds calldata for buy(uint256,uint256,address,uint64).
func (v Vault) PackBuy(tokenAmount, maxQuoteAmount *big.Int, recipient common.Address, deadline uint64) ([]byte, error) {
	return v.ABI.Pack("buy", tokenAmount, maxQuoteAmount, recipient, deadline)
}

// PackRequestPurchase builds calldata for requestPurchase(uint256,uint256,address,uint64)
func (v Vault) PackRequestPurchase(tokenAmount, maxQuoteAmount *big.Int, recipient common.Address, deadline uint64) ([]byte, error) {
	return v.ABI.Pack("requestPurchase", tokenAmount, maxQuoteAmount, recipient, deadline)
}

// PackSettlePurchase builds calldata for settlePurchase(uint256)
func (v Vault) PackSettlePurchase(id *big.Int) ([]byte, error) {
	return v.ABI.Pack("settlePurchase", id)
}

// PackCancelPurchase builds calldata for cancelPurchase(uint256)
func (v Vault) PackCancelPurchase(id *big.Int) ([]byte, error) {
	return v.ABI.Pack("cancelPurchase", id)
}

// PackRejectPurchase builds calldata for rejectPurchase(uint256)
func (v Vault) PackRejectPurchase(id *big.Int) ([]byte, error) {
	return v.ABI.Pack("rejectPurchase", id)
}

// PurchaseOrderTuple mirrors IVault.PurchaseOrder; BaseRound is a uint80
type PurchaseOrderTuple struct {
	Buyer          common.Address `abi:"buyer"`
	CreatedAt      uint64         `abi:"createdAt"`
	Status         uint8          `abi:"status"`
	Recipient      common.Address `abi:"recipient"`
	Strategy       common.Address `abi:"strategy"`
	BaseRound      *big.Int       `abi:"baseRound"`
	TokenAmount    *big.Int       `abi:"tokenAmount"`
	MaxQuoteAmount *big.Int       `abi:"maxQuoteAmount"`
}

// PackGetPurchase builds calldata for the getPurchase(uint256) view call
func (v Vault) PackGetPurchase(id *big.Int) ([]byte, error) { return v.ABI.Pack("getPurchase", id) }

// UnpackGetPurchase decodes the return value of getPurchase
func (v Vault) UnpackGetPurchase(data []byte) (PurchaseOrderTuple, error) {
	out, err := v.ABI.Unpack("getPurchase", data)
	if err != nil {
		return PurchaseOrderTuple{}, err
	}
	return *abi.ConvertType(out[0], new(PurchaseOrderTuple)).(*PurchaseOrderTuple), nil
}

// PackPendingPurchaseQuote builds calldata for the pendingPurchaseQuote() view call
func (v Vault) PackPendingPurchaseQuote() ([]byte, error) { return v.ABI.Pack("pendingPurchaseQuote") }

// UnpackPendingPurchaseQuote decodes the return value of pendingPurchaseQuote()
func (v Vault) UnpackPendingPurchaseQuote(data []byte) (*big.Int, error) {
	out, err := v.ABI.Unpack("pendingPurchaseQuote", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PackNextPurchaseId builds calldata for the nextPurchaseId() view call
func (v Vault) PackNextPurchaseId() ([]byte, error) { return v.ABI.Pack("nextPurchaseId") }

// UnpackNextPurchaseId decodes the return value of nextPurchaseId()
func (v Vault) UnpackNextPurchaseId(data []byte) (*big.Int, error) {
	out, err := v.ABI.Unpack("nextPurchaseId", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PackInventory builds calldata for the inventory() view call.
func (v Vault) PackInventory() ([]byte, error) { return v.ABI.Pack("inventory") }

// UnpackInventory decodes the return value of inventory().
func (v Vault) UnpackInventory(data []byte) (*big.Int, error) {
	out, err := v.ABI.Unpack("inventory", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PackStrategy builds calldata for the strategy() view call.
func (v Vault) PackStrategy() ([]byte, error) { return v.ABI.Pack("strategy") }

// UnpackStrategy decodes the return value of strategy().
func (v Vault) UnpackStrategy(data []byte) (common.Address, error) {
	out, err := v.ABI.Unpack("strategy", data)
	if err != nil {
		return common.Address{}, err
	}
	return *abi.ConvertType(out[0], new(common.Address)).(*common.Address), nil
}

// MinPurchaseQuoteChangedEvent mirrors the MinPurchaseQuoteChanged event: previous and
// new minimums in log.Data, the indexed caller in topics
type MinPurchaseQuoteChangedEvent struct {
	PreviousMinimum *big.Int
	NewMinimum      *big.Int
	Caller          common.Address
}

// UnpackMinPurchaseQuoteChanged decodes a MinPurchaseQuoteChanged log
func (v Vault) UnpackMinPurchaseQuoteChanged(data []byte, topics []common.Hash) (MinPurchaseQuoteChangedEvent, error) {
	var partial struct {
		PreviousMinimum *big.Int
		NewMinimum      *big.Int
	}
	if err := v.ABI.UnpackIntoInterface(&partial, "MinPurchaseQuoteChanged", data); err != nil {
		return MinPurchaseQuoteChangedEvent{}, err
	}
	ev := MinPurchaseQuoteChangedEvent{PreviousMinimum: partial.PreviousMinimum, NewMinimum: partial.NewMinimum}
	if len(topics) >= 2 {
		ev.Caller = common.HexToAddress(topics[1].Hex())
	}
	return ev, nil
}

// PackPreviewBuy builds calldata for previewBuy(uint256).
func (v Vault) PackPreviewBuy(tokenAmount *big.Int) ([]byte, error) {
	return v.ABI.Pack("previewBuy", tokenAmount)
}

// UnpackPreviewBuy decodes the return value of previewBuy().
func (v Vault) UnpackPreviewBuy(data []byte) (*big.Int, error) {
	out, err := v.ABI.Unpack("previewBuy", data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PurchasedEvent mirrors the Purchased event.
type PurchasedEvent struct {
	Buyer       common.Address
	Recipient   common.Address
	TokenAmount *big.Int
	QuoteAmount *big.Int
	Strategy    common.Address
}

// UnpackPurchased decodes a Purchased log.
func (v Vault) UnpackPurchased(data []byte, topics []common.Hash) (PurchasedEvent, error) {
	var partial struct {
		TokenAmount *big.Int
		QuoteAmount *big.Int
		Strategy    common.Address
	}
	if err := v.ABI.UnpackIntoInterface(&partial, "Purchased", data); err != nil {
		return PurchasedEvent{}, err
	}
	ev := PurchasedEvent{TokenAmount: partial.TokenAmount, QuoteAmount: partial.QuoteAmount, Strategy: partial.Strategy}
	if len(topics) >= 3 {
		ev.Buyer = common.HexToAddress(topics[1].Hex())
		ev.Recipient = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// PurchaseRequestedEvent mirrors the PurchaseRequested event: id, buyer and recipient are
// indexed; BaseRound is the feed's uint80 round id
type PurchaseRequestedEvent struct {
	ID             *big.Int
	Buyer          common.Address
	Recipient      common.Address
	TokenAmount    *big.Int
	MaxQuoteAmount *big.Int
	Strategy       common.Address
	BaseRound      *big.Int
	CreatedAt      uint64
}

// UnpackPurchaseRequested decodes a PurchaseRequested log
func (v Vault) UnpackPurchaseRequested(data []byte, topics []common.Hash) (PurchaseRequestedEvent, error) {
	var partial struct {
		TokenAmount    *big.Int
		MaxQuoteAmount *big.Int
		Strategy       common.Address
		BaseRound      *big.Int
		CreatedAt      uint64
	}
	if err := v.ABI.UnpackIntoInterface(&partial, "PurchaseRequested", data); err != nil {
		return PurchaseRequestedEvent{}, err
	}
	ev := PurchaseRequestedEvent{
		TokenAmount:    partial.TokenAmount,
		MaxQuoteAmount: partial.MaxQuoteAmount,
		Strategy:       partial.Strategy,
		BaseRound:      partial.BaseRound,
		CreatedAt:      partial.CreatedAt,
	}
	if len(topics) >= 4 {
		ev.ID = topics[1].Big()
		ev.Buyer = common.HexToAddress(topics[2].Hex())
		ev.Recipient = common.HexToAddress(topics[3].Hex())
	}
	return ev, nil
}

// PurchasePricedEvent mirrors PurchaseSettled and PurchaseRefunded: RoundID is the round
// that priced the order, QuoteAmount the quote at that round, Refund the deposit returned
type PurchasePricedEvent struct {
	ID          *big.Int
	RoundID     *big.Int
	QuoteAmount *big.Int
	Refund      *big.Int
}

// UnpackPurchasePriced decodes a PurchaseSettled or PurchaseRefunded log; name selects the
// event, and both have the same fields
func (v Vault) UnpackPurchasePriced(name string, data []byte, topics []common.Hash) (PurchasePricedEvent, error) {
	var partial struct {
		RoundId     *big.Int
		QuoteAmount *big.Int
		Refund      *big.Int
	}
	if err := v.ABI.UnpackIntoInterface(&partial, name, data); err != nil {
		return PurchasePricedEvent{}, err
	}
	ev := PurchasePricedEvent{RoundID: partial.RoundId, QuoteAmount: partial.QuoteAmount, Refund: partial.Refund}
	if len(topics) >= 2 {
		ev.ID = topics[1].Big()
	}
	return ev, nil
}

// PurchaseClosedEvent mirrors PurchaseCancelled and PurchaseRejected: Refund is the whole
// deposit returned to Buyer
type PurchaseClosedEvent struct {
	ID     *big.Int
	Buyer  common.Address
	Refund *big.Int
}

// UnpackPurchaseClosed decodes a PurchaseCancelled or PurchaseRejected log; name selects
// the event, and both have the same fields
func (v Vault) UnpackPurchaseClosed(name string, data []byte, topics []common.Hash) (PurchaseClosedEvent, error) {
	var partial struct{ Refund *big.Int }
	if err := v.ABI.UnpackIntoInterface(&partial, name, data); err != nil {
		return PurchaseClosedEvent{}, err
	}
	ev := PurchaseClosedEvent{Refund: partial.Refund}
	if len(topics) >= 3 {
		ev.ID = topics[1].Big()
		ev.Buyer = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// ProceedsWithdrawnEvent mirrors the ProceedsWithdrawn event: the treasury paid
// (indexed), the quote-token amount withdrawn (non-indexed) and the caller
// (indexed)
type ProceedsWithdrawnEvent struct {
	Treasury    common.Address
	QuoteAmount *big.Int
	Caller      common.Address
}

// UnpackProceedsWithdrawn reads treasury and caller from topics 1 and 2, and
// quoteAmount from log.Data
func (v Vault) UnpackProceedsWithdrawn(data []byte, topics []common.Hash) (ProceedsWithdrawnEvent, error) {
	var partial struct{ QuoteAmount *big.Int }
	if err := v.ABI.UnpackIntoInterface(&partial, "ProceedsWithdrawn", data); err != nil {
		return ProceedsWithdrawnEvent{}, err
	}
	ev := ProceedsWithdrawnEvent{QuoteAmount: partial.QuoteAmount}
	if len(topics) >= 3 {
		ev.Treasury = common.HexToAddress(topics[1].Hex())
		ev.Caller = common.HexToAddress(topics[2].Hex())
	}
	return ev, nil
}

// AddressChangeEvent mirrors StrategyChanged and TreasuryChanged: previous address, new
// address and caller, all indexed
type AddressChangeEvent struct {
	Previous common.Address
	New      common.Address
	Caller   common.Address
}

// UnpackStrategyChanged decodes a StrategyChanged log from its topics
// (previousStrategy, newStrategy and caller, all indexed)
func (v Vault) UnpackStrategyChanged(topics []common.Hash) AddressChangeEvent {
	return unpackAddressChange(topics)
}

// UnpackTreasuryChanged decodes a TreasuryChanged log from its topics
// (previousTreasury, newTreasury and caller, all indexed)
func (v Vault) UnpackTreasuryChanged(topics []common.Hash) AddressChangeEvent {
	return unpackAddressChange(topics)
}

func unpackAddressChange(topics []common.Hash) AddressChangeEvent {
	var ev AddressChangeEvent
	if len(topics) >= 4 {
		ev.Previous = common.HexToAddress(topics[1].Hex())
		ev.New = common.HexToAddress(topics[2].Hex())
		ev.Caller = common.HexToAddress(topics[3].Hex())
	}
	return ev
}

// EventID returns the keccak256 topic0 for the named event.
func (v Vault) EventID(name string) common.Hash { return v.ABI.Events[name].ID }
