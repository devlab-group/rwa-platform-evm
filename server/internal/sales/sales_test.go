package sales

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
)

func hexKey(data []byte) string { return common.Bytes2Hex(data) }

func TestGetInventory(t *testing.T) {
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	quoteAddr := common.HexToAddress("0x0000000000000000000000000000000000000B")
	strategyAddr := common.HexToAddress("0x0000000000000000000000000000000000000C")

	client := blockchain.NewFakeClient()
	vaultABI := bindings.NewVault()
	erc20ABI := bindings.NewERC20()
	strategyABI := bindings.NewFixedPriceStrategy()

	invData, _ := vaultABI.PackInventory()
	invReturn, _ := vaultABI.ABI.Methods["inventory"].Outputs.Pack(big.NewInt(500000))
	client.CallResponses[hexKey(invData)] = invReturn

	balData, _ := erc20ABI.PackBalanceOf(vaultAddr)
	balReturn, _ := erc20ABI.ABI.Methods["balanceOf"].Outputs.Pack(big.NewInt(1000000))
	client.CallResponses[hexKey(balData)] = balReturn

	purchaseData, _ := strategyABI.PackPurchasePricePerWholeToken()
	purchaseReturn, _ := strategyABI.ABI.Methods["purchasePricePerWholeToken"].Outputs.Pack(big.NewInt(2000000))
	client.CallResponses[hexKey(purchaseData)] = purchaseReturn

	redeemData, _ := strategyABI.PackRedemptionPricePerWholeToken()
	redeemReturn, _ := strategyABI.ABI.Methods["redemptionPricePerWholeToken"].Outputs.Pack(big.NewInt(1950000))
	client.CallResponses[hexKey(redeemData)] = redeemReturn

	svc := New(client, vaultAddr, quoteAddr, strategyAddr, memory.NewPurchaseRepository())
	inv, err := svc.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if inv.Inventory != "500000" {
		t.Errorf("Inventory = %s", inv.Inventory)
	}
	if inv.QuoteBalance != "1000000" {
		t.Errorf("QuoteBalance = %s", inv.QuoteBalance)
	}
	if inv.PurchasePrice != "2000000" {
		t.Errorf("PurchasePrice = %s", inv.PurchasePrice)
	}
	if inv.RedemptionPrice != "1950000" {
		t.Errorf("RedemptionPrice = %s", inv.RedemptionPrice)
	}
}
