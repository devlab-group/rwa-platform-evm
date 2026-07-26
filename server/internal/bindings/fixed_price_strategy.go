package bindings

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const fixedPriceStrategyABIJSON = `[
  {"type":"function","name":"quotePurchase","stateMutability":"view","inputs":[{"name":"tokenAmount","type":"uint256"}],"outputs":[{"name":"quoteAmount","type":"uint256"}]},
  {"type":"function","name":"quoteRedemption","stateMutability":"view","inputs":[{"name":"tokenAmount","type":"uint256"}],"outputs":[{"name":"quoteAmount","type":"uint256"}]},
  {"type":"function","name":"previewBuy","stateMutability":"view","inputs":[{"name":"tokenAmount","type":"uint256"}],"outputs":[{"name":"quoteAmount","type":"uint256"}]},
  {"type":"function","name":"previewRedeem","stateMutability":"view","inputs":[{"name":"tokenAmount","type":"uint256"}],"outputs":[{"name":"quoteAmount","type":"uint256"}]},
  {"type":"function","name":"setPurchasePrice","stateMutability":"nonpayable","inputs":[{"name":"newPrice","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"setRedemptionPrice","stateMutability":"nonpayable","inputs":[{"name":"newPrice","type":"uint256"}],"outputs":[]},
  {"type":"function","name":"purchasePricePerWholeToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"redemptionPricePerWholeToken","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"tokenDecimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]},
  {"type":"event","name":"PurchasePriceUpdated","anonymous":false,"inputs":[
    {"name":"previousPrice","type":"uint256","indexed":false},{"name":"newPrice","type":"uint256","indexed":false},
    {"name":"caller","type":"address","indexed":true}]},
  {"type":"event","name":"RedemptionPriceUpdated","anonymous":false,"inputs":[
    {"name":"previousPrice","type":"uint256","indexed":false},{"name":"newPrice","type":"uint256","indexed":false},
    {"name":"caller","type":"address","indexed":true}]}
]`

// FixedPriceStrategy encodes calldata / decodes reads for IFixedPriceStrategy.
type FixedPriceStrategy struct{ ABI abi.ABI }

// NewFixedPriceStrategy parses the FixedPriceStrategy ABI once.
func NewFixedPriceStrategy() FixedPriceStrategy {
	return FixedPriceStrategy{ABI: mustABI(fixedPriceStrategyABIJSON)}
}

// PackPreviewBuy builds calldata for previewBuy(uint256).
func (f FixedPriceStrategy) PackPreviewBuy(tokenAmount *big.Int) ([]byte, error) {
	return f.ABI.Pack("previewBuy", tokenAmount)
}

// PackPreviewRedeem builds calldata for previewRedeem(uint256).
func (f FixedPriceStrategy) PackPreviewRedeem(tokenAmount *big.Int) ([]byte, error) {
	return f.ABI.Pack("previewRedeem", tokenAmount)
}

// UnpackQuote decodes a single uint256 quoteAmount return value, shared by
// previewBuy/previewRedeem/quotePurchase/quoteRedemption.
func (f FixedPriceStrategy) UnpackQuote(method string, data []byte) (*big.Int, error) {
	out, err := f.ABI.Unpack(method, data)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// PackSetPurchasePrice builds calldata for setPurchasePrice(uint256).
func (f FixedPriceStrategy) PackSetPurchasePrice(newPrice *big.Int) ([]byte, error) {
	return f.ABI.Pack("setPurchasePrice", newPrice)
}

// PackSetRedemptionPrice builds calldata for setRedemptionPrice(uint256).
func (f FixedPriceStrategy) PackSetRedemptionPrice(newPrice *big.Int) ([]byte, error) {
	return f.ABI.Pack("setRedemptionPrice", newPrice)
}

// PackPurchasePricePerWholeToken builds calldata for the view call.
func (f FixedPriceStrategy) PackPurchasePricePerWholeToken() ([]byte, error) {
	return f.ABI.Pack("purchasePricePerWholeToken")
}

// PackRedemptionPricePerWholeToken builds calldata for the view call.
func (f FixedPriceStrategy) PackRedemptionPricePerWholeToken() ([]byte, error) {
	return f.ABI.Pack("redemptionPricePerWholeToken")
}

// PriceUpdatedEvent mirrors the PurchasePriceUpdated / RedemptionPriceUpdated
// events: previousPrice/newPrice are non-indexed (in log.Data), caller is
// indexed (in log.Topics).
type PriceUpdatedEvent struct {
	PreviousPrice *big.Int
	NewPrice      *big.Int
	Caller        common.Address
}

// UnpackPriceUpdated decodes a PurchasePriceUpdated or RedemptionPriceUpdated
// log (name selects which; they share the same field layout).
func (f FixedPriceStrategy) UnpackPriceUpdated(name string, data []byte, topics []common.Hash) (PriceUpdatedEvent, error) {
	var partial struct {
		PreviousPrice *big.Int
		NewPrice      *big.Int
	}
	if err := f.ABI.UnpackIntoInterface(&partial, name, data); err != nil {
		return PriceUpdatedEvent{}, err
	}
	ev := PriceUpdatedEvent{PreviousPrice: partial.PreviousPrice, NewPrice: partial.NewPrice}
	if len(topics) >= 2 {
		ev.Caller = common.HexToAddress(topics[1].Hex())
	}
	return ev, nil
}

// EventID returns the keccak256 topic0 for the named event.
func (f FixedPriceStrategy) EventID(name string) common.Hash { return f.ABI.Events[name].ID }
