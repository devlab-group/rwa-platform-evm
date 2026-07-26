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
  {"type":"event","name":"Purchased","anonymous":false,"inputs":[
    {"name":"buyer","type":"address","indexed":true},{"name":"recipient","type":"address","indexed":true},
    {"name":"tokenAmount","type":"uint256","indexed":false},{"name":"quoteAmount","type":"uint256","indexed":false},
    {"name":"strategy","type":"address","indexed":false}]},
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

// NewVault parses the Vault ABI once.
func NewVault() Vault { return Vault{ABI: mustABI(vaultABIJSON)} }

// PackBuy builds calldata for buy(uint256,uint256,address,uint64).
func (v Vault) PackBuy(tokenAmount, maxQuoteAmount *big.Int, recipient common.Address, deadline uint64) ([]byte, error) {
	return v.ABI.Pack("buy", tokenAmount, maxQuoteAmount, recipient, deadline)
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

// ProceedsWithdrawnEvent mirrors the ProceedsWithdrawn event: the treasury
// paid (indexed), the quote-token amount withdrawn (non-indexed), and the
// caller that triggered it (indexed).
type ProceedsWithdrawnEvent struct {
	Treasury    common.Address
	QuoteAmount *big.Int
	Caller      common.Address
}

// UnpackProceedsWithdrawn decodes a ProceedsWithdrawn log. treasury and
// caller are indexed (topics 1 and 2, in declaration order); quoteAmount is
// the only non-indexed field, decoded from log.Data.
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

// AddressChangeEvent mirrors the (all-indexed) fields of Vault's
// StrategyChanged / TreasuryChanged events: the previous and new address plus
// the caller that made the change.
type AddressChangeEvent struct {
	Previous common.Address
	New      common.Address
	Caller   common.Address
}

// UnpackStrategyChanged decodes a StrategyChanged log from its topics
// (previousStrategy, newStrategy, caller — all indexed).
func (v Vault) UnpackStrategyChanged(topics []common.Hash) AddressChangeEvent {
	return unpackAddressChange(topics)
}

// UnpackTreasuryChanged decodes a TreasuryChanged log from its topics
// (previousTreasury, newTreasury, caller — all indexed).
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
