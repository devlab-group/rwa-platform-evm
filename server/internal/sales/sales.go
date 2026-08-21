// Package sales provides read models for the Vault contract: inventory and
// the indexed purchase history. Per-amount purchase quotes are read straight
// from Vault.previewBuy by the client. It submits no
// transactions and builds none: an investor's Vault.buy and the issuer's
// treasury withdrawal are both assembled and signed by the wallet/multisig
// itself, never by a server hot key. The off-chain payment-distribution
// feature (Vault.distribute, the distributor hot key) has been removed
// from the platform — only on-chain purchases remain.
package sales

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Inventory mirrors api Schemas.Inventory.
type Inventory struct {
	Inventory       string `json:"inventory"`
	QuoteBalance    string `json:"quoteBalance"`
	PurchasePrice   string `json:"purchasePrice"`
	RedemptionPrice string `json:"redemptionPrice"`
}

// PurchaseView mirrors api Schemas.Purchase.
type PurchaseView struct {
	TxHash        string `json:"txHash"`
	Buyer         string `json:"buyer"`
	Recipient     string `json:"recipient"`
	TokenAmount   string `json:"tokenAmount"`
	QuoteAmount   string `json:"quoteAmount"`
	BlockNumber   uint64 `json:"blockNumber"`
	Confirmations uint64 `json:"confirmations"`
}

// Service reads Vault/strategy state. It submits no transactions itself:
// every Vault write (investor buy, and — now that off-chain distribution is
// removed and treasury withdrawal is submitted directly by the treasurer's
// wallet in the web — treasury withdrawal too) is a wallet/multisig
// transaction, never a server hot-key action.
type Service struct {
	client         blockchain.Client
	vault          bindings.Vault
	erc20          bindings.ERC20
	strategy       bindings.FixedPriceStrategy
	vaultAddr      common.Address
	quoteTokenAddr common.Address
	purchases      repository.PurchaseRepository
}

// New constructs a sales Service. purchases may be nil if the caller never
// calls ListPurchases/Reconcile. The strategy is deliberately NOT a
// parameter — see strategyAddress.
func New(client blockchain.Client, vaultAddr, quoteTokenAddr common.Address, purchases repository.PurchaseRepository) *Service {
	return &Service{
		client: client,
		vault:  bindings.NewVault(), erc20: bindings.NewERC20(), strategy: bindings.NewFixedPriceStrategy(),
		vaultAddr: vaultAddr, quoteTokenAddr: quoteTokenAddr,
		purchases: purchases,
	}
}

// strategyAddress reads the Vault's CURRENT pricing strategy rather than
// trusting the one recorded at deployment. Vault.setStrategy is
// admin-callable (unlike RedemptionEscrow's immutable strategy), so the
// deployed address is a snapshot, not an invariant: an admin who swaps the
// strategy would otherwise leave this public, unauthenticated price feed
// quoting a contract that the investor's own client-side Vault.previewBuy
// no longer routes through — the server advertising one price while the
// buy executes at another.
func (s *Service) strategyAddress(ctx context.Context) (common.Address, error) {
	data, err := s.vault.PackStrategy()
	if err != nil {
		return common.Address{}, err
	}
	out, err := blockchain.Call(ctx, s.client, s.vaultAddr, data)
	if err != nil {
		return common.Address{}, fmt.Errorf("sales: Vault.strategy: %w", err)
	}
	return s.vault.UnpackStrategy(out)
}

// GetInventory reads Vault inventory, the Vault's quote-token balance, and
// the strategy's current purchase/redemption prices.
func (s *Service) GetInventory(ctx context.Context) (Inventory, error) {
	invData, err := s.vault.PackInventory()
	if err != nil {
		return Inventory{}, err
	}
	invOut, err := blockchain.Call(ctx, s.client, s.vaultAddr, invData)
	if err != nil {
		return Inventory{}, fmt.Errorf("sales: Vault.inventory: %w", err)
	}
	inventory, err := s.vault.UnpackInventory(invOut)
	if err != nil {
		return Inventory{}, err
	}

	balData, err := s.erc20.PackBalanceOf(s.vaultAddr)
	if err != nil {
		return Inventory{}, err
	}
	balOut, err := blockchain.Call(ctx, s.client, s.quoteTokenAddr, balData)
	if err != nil {
		return Inventory{}, fmt.Errorf("sales: quoteToken.balanceOf: %w", err)
	}
	quoteBalance, err := s.erc20.UnpackBalanceOf(balOut)
	if err != nil {
		return Inventory{}, err
	}

	strategyAddr, err := s.strategyAddress(ctx)
	if err != nil {
		return Inventory{}, err
	}
	purchasePrice, err := s.readStrategyPrice(ctx, strategyAddr, "purchasePricePerWholeToken")
	if err != nil {
		return Inventory{}, err
	}
	redemptionPrice, err := s.readStrategyPrice(ctx, strategyAddr, "redemptionPricePerWholeToken")
	if err != nil {
		return Inventory{}, err
	}

	return Inventory{
		Inventory:       inventory.String(),
		QuoteBalance:    quoteBalance.String(),
		PurchasePrice:   purchasePrice.String(),
		RedemptionPrice: redemptionPrice.String(),
	}, nil
}

func (s *Service) readStrategyPrice(ctx context.Context, strategyAddr common.Address, method string) (*big.Int, error) {
	var data []byte
	var err error
	switch method {
	case "purchasePricePerWholeToken":
		data, err = s.strategy.PackPurchasePricePerWholeToken()
	case "redemptionPricePerWholeToken":
		data, err = s.strategy.PackRedemptionPricePerWholeToken()
	default:
		return nil, fmt.Errorf("sales: unknown strategy method %q", method)
	}
	if err != nil {
		return nil, err
	}
	out, err := blockchain.Call(ctx, s.client, strategyAddr, data)
	if err != nil {
		return nil, fmt.Errorf("sales: strategy.%s: %w", method, err)
	}
	return s.strategy.UnpackQuote(method, out)
}

// ListPurchases returns one bounded, newest-block-first page of indexed
// Vault.buy fills using repository-level keyset pagination (see
// repository.PurchaseRepository.ListPage's doc comment), with
// confirmations derived from currentBlock (typically the indexer's
// last-scanned block). cursor is "" for the first page.
func (s *Service) ListPurchases(ctx context.Context, currentBlock uint64, cursor string, limit int) ([]PurchaseView, string, error) {
	items, next, err := s.purchases.ListPage(ctx, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	out := make([]PurchaseView, len(items))
	for i, p := range items {
		out[i] = PurchaseView{
			TxHash: p.TxHash, Buyer: p.Buyer, Recipient: p.Recipient,
			TokenAmount: p.TokenAmount, QuoteAmount: p.QuoteAmount,
			BlockNumber: p.BlockNumber, Confirmations: confirmationsOf(currentBlock, p.BlockNumber),
		}
	}
	return out, next, nil
}

func confirmationsOf(currentBlock, blockNumber uint64) uint64 {
	if currentBlock < blockNumber {
		return 0
	}
	return currentBlock - blockNumber
}
