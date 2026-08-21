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

	stratData, _ := vaultABI.PackStrategy()
	stratReturn, _ := vaultABI.ABI.Methods["strategy"].Outputs.Pack(strategyAddr)
	client.CallResponses[hexKey(stratData)] = stratReturn

	svc := New(client, vaultAddr, quoteAddr, memory.NewPurchaseRepository())
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

// TestGetInventoryPricesFromTheVaultsCurrentStrategy pins the fix for a
// swapped strategy: Vault.setStrategy is admin-callable, so the strategy
// recorded at deploy time can stop being the one the Vault actually prices
// through. The public inventory feed must follow the Vault, or it quotes a
// price the investor's own client-side previewBuy will not honour.
//
// The assertion is on the call TARGET, not the returned numbers: the fake
// client keys its canned responses by calldata alone, so both strategies
// answer purchasePricePerWholeToken() identically and only the address the
// read went to can distinguish them.
func TestGetInventoryPricesFromTheVaultsCurrentStrategy(t *testing.T) {
	vaultAddr := common.HexToAddress("0x0000000000000000000000000000000000000A")
	quoteAddr := common.HexToAddress("0x0000000000000000000000000000000000000B")
	deployedStrategy := common.HexToAddress("0x0000000000000000000000000000000000000C")
	currentStrategy := common.HexToAddress("0x0000000000000000000000000000000000000D")

	client := blockchain.NewFakeClient()
	vaultABI := bindings.NewVault()
	erc20ABI := bindings.NewERC20()
	strategyABI := bindings.NewFixedPriceStrategy()

	invData, _ := vaultABI.PackInventory()
	invReturn, _ := vaultABI.ABI.Methods["inventory"].Outputs.Pack(big.NewInt(1))
	client.CallResponses[hexKey(invData)] = invReturn
	balData, _ := erc20ABI.PackBalanceOf(vaultAddr)
	balReturn, _ := erc20ABI.ABI.Methods["balanceOf"].Outputs.Pack(big.NewInt(1))
	client.CallResponses[hexKey(balData)] = balReturn
	purchaseData, _ := strategyABI.PackPurchasePricePerWholeToken()
	purchaseReturn, _ := strategyABI.ABI.Methods["purchasePricePerWholeToken"].Outputs.Pack(big.NewInt(3))
	client.CallResponses[hexKey(purchaseData)] = purchaseReturn
	redeemData, _ := strategyABI.PackRedemptionPricePerWholeToken()
	redeemReturn, _ := strategyABI.ABI.Methods["redemptionPricePerWholeToken"].Outputs.Pack(big.NewInt(2))
	client.CallResponses[hexKey(redeemData)] = redeemReturn

	// The Vault has since been pointed at a different strategy.
	stratData, _ := vaultABI.PackStrategy()
	stratReturn, _ := vaultABI.ABI.Methods["strategy"].Outputs.Pack(currentStrategy)
	client.CallResponses[hexKey(stratData)] = stratReturn

	svc := New(client, vaultAddr, quoteAddr, memory.NewPurchaseRepository())
	if _, err := svc.GetInventory(context.Background()); err != nil {
		t.Fatalf("GetInventory: %v", err)
	}

	for _, data := range [][]byte{purchaseData, redeemData} {
		var targets []common.Address
		for _, call := range client.Calls {
			if hexKey(call.Data) == hexKey(data) && call.To != nil {
				targets = append(targets, *call.To)
			}
		}
		if len(targets) != 1 {
			t.Fatalf("price read happened %d times, want once", len(targets))
		}
		if targets[0] == deployedStrategy {
			t.Errorf("price was read from the strategy recorded at deploy time (%s), not the Vault's current one (%s)", deployedStrategy.Hex(), currentStrategy.Hex())
		}
		if targets[0] != currentStrategy {
			t.Errorf("price read went to %s, want the Vault's current strategy %s", targets[0].Hex(), currentStrategy.Hex())
		}
	}
}
