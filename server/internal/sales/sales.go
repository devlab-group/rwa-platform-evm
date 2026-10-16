// Package sales reads Vault inventory and indexed purchase history. Clients quote via
// Vault.previewBuy; investor wallets build/sign Vault.buy and treasurer wallets or
// multisigs build/sign withdrawals. This package builds or submits no transactions.
package sales

import (
	"context"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Inventory mirrors api Schemas.Inventory.
type Inventory struct {
	Inventory    string `json:"inventory"`
	QuoteBalance string `json:"quoteBalance"`
	// PurchasePrice and RedemptionPrice are omitted when an Oracle strategy's read fails;
	// PriceError then names why
	PurchasePrice   string `json:"purchasePrice,omitempty"`
	RedemptionPrice string `json:"redemptionPrice,omitempty"`
	PriceError      string `json:"priceError,omitempty"`
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

// Service reads Vault/strategy state and maintains purchase history without submitting
// transactions
type Service struct {
	client         blockchain.Client
	vault          bindings.Vault
	erc20          bindings.ERC20
	strategy       bindings.FixedPriceStrategy
	oracle         bindings.OraclePriceStrategy
	aggregator     bindings.AggregatorV3
	vaultAddr      common.Address
	quoteTokenAddr common.Address
	purchases      repository.PurchaseRepository

	// kinds caches StrategyKind answers and oracleCfg the immutable OraclePriceStrategy
	// views, both by strategy address
	kinds     sync.Map
	oracleCfg sync.Map
}

// New constructs a Service; purchases may be nil if ListPurchases and Reconcile are
// unused. strategyAddress resolves the strategy live.
func New(client blockchain.Client, vaultAddr, quoteTokenAddr common.Address, purchases repository.PurchaseRepository) *Service {
	return &Service{
		client: client,
		vault:  bindings.NewVault(), erc20: bindings.NewERC20(), strategy: bindings.NewFixedPriceStrategy(),
		oracle: bindings.NewOraclePriceStrategy(), aggregator: bindings.NewAggregatorV3(),
		vaultAddr: vaultAddr, quoteTokenAddr: quoteTokenAddr,
		purchases: purchases,
	}
}

// strategyAddress reads live Vault.strategy so inventory prices follow the contract
// used by buy/previewBuy after an admin's setStrategy. The deploy-time address can
// become stale; an rwa-v2 RedemptionEscrow keeps its deploy-time strategy, while an
// rwa-v3 escrow reads Vault.strategy() too.
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

// GetInventory reads the Vault's inventory and quote-token balance and the
// current strategy's purchase and redemption prices
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
	if s.StrategyKind(ctx, strategyAddr) == KindOracle {
		prices := s.OraclePrices(ctx, strategyAddr)
		inv := Inventory{Inventory: inventory.String(), QuoteBalance: quoteBalance.String(), PriceError: prices.Error}
		if prices.Purchase != nil {
			inv.PurchasePrice = prices.Purchase.String()
		}
		if prices.Redemption != nil {
			inv.RedemptionPrice = prices.Redemption.String()
		}
		return inv, nil
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

// ListPurchases keyset-paginates indexed Vault.buy fills by newest block
// (repository.PurchaseRepository.ListPage); empty cursor starts the first page.
// Confirmations use currentBlock, usually the indexer checkpoint.
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
