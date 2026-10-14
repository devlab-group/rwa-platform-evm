// Package sales provides read models for the Vault contract: inventory and the
// indexed purchase history. Clients read per-amount purchase quotes from
// Vault.previewBuy themselves. The package neither builds nor submits
// transactions: the investor's Vault.buy and the treasurer's withdrawal are
// assembled and signed in the wallet or multisig.
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

// Service reads Vault and strategy state and maintains the purchases read
// model. It submits no transactions.
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
// calls ListPurchases or Reconcile. The strategy address is not a parameter;
// see strategyAddress.
func New(client blockchain.Client, vaultAddr, quoteTokenAddr common.Address, purchases repository.PurchaseRepository) *Service {
	return &Service{
		client: client,
		vault:  bindings.NewVault(), erc20: bindings.NewERC20(), strategy: bindings.NewFixedPriceStrategy(),
		vaultAddr: vaultAddr, quoteTokenAddr: quoteTokenAddr,
		purchases: purchases,
	}
}

// strategyAddress reads the Vault's current pricing strategy instead of using
// the address recorded at deployment. Vault.setStrategy is callable by the
// admin (RedemptionEscrow's strategy is immutable), so the deployed address can
// go stale. Reading it live keeps the public inventory prices in line with what
// Vault.buy and Vault.previewBuy use.
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

// ListPurchases returns one page of indexed Vault.buy fills, newest block
// first, using keyset pagination (see repository.PurchaseRepository.ListPage).
// Confirmations are derived from currentBlock, typically the indexer's last
// scanned block. cursor is "" for the first page.
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
