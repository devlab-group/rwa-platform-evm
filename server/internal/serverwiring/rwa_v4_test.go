package serverwiring

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
)

func TestKnownEventNamesCoverTheForwardPricingEvents(t *testing.T) {
	known := KnownEventNames(v3Addrs(), "")
	for _, n := range []string{
		"PurchaseRequested",
		"PurchaseSettled",
		"PurchaseRefunded",
		"PurchaseCancelled",
		"PurchaseRejected",
		"RedemptionForwardPriced",
		"RedemptionPriced",
		"Purchased",
		"RedemptionRequested",
	} {
		if !known[n] {
			t.Errorf("%s is not a known event for a v4 stack", n)
		}
	}
	// the events belong to their contracts: with no Vault there are no order events
	noVault := v3Addrs()
	noVault.Vault = ""
	if KnownEventNames(noVault, "")["PurchaseRequested"] {
		t.Error("PurchaseRequested is known without a Vault")
	}
	noEscrow := v3Addrs()
	noEscrow.RedemptionEscrow = ""
	if KnownEventNames(noEscrow, "")["RedemptionPriced"] {
		t.Error("RedemptionPriced is known without an escrow")
	}
}

// The live decoder and the DLQ retry decoder must both type the new logs by emitting contract
func TestDecodersRouteTheForwardPricingLogsByEmitter(t *testing.T) {
	addrs := v3Addrs()
	buyer := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	vault, escrow := bindings.NewVault(), bindings.NewRedemptionEscrow()
	round, _ := new(big.Int).SetString("36893488147419103233", 10)

	pr := vault.ABI.Events["PurchaseRequested"]
	prData, err := pr.Inputs.NonIndexed().Pack(big.NewInt(1000), big.NewInt(2500), common.HexToAddress("0x57"), round, uint64(1700000000))
	if err != nil {
		t.Fatal(err)
	}
	purchase := types.Log{
		Address: common.HexToAddress(addrs.Vault),
		Data:    prData,
		Topics:  []common.Hash{pr.ID, common.BigToHash(big.NewInt(3)), common.BytesToHash(buyer.Bytes()), common.BytesToHash(buyer.Bytes())},
	}
	rp := escrow.ABI.Events["RedemptionPriced"]
	rpData, err := rp.Inputs.NonIndexed().Pack(round, big.NewInt(950))
	if err != nil {
		t.Fatal(err)
	}
	redemption := types.Log{Address: common.HexToAddress(addrs.RedemptionEscrow), Data: rpData, Topics: []common.Hash{rp.ID, common.BigToHash(big.NewInt(4))}}

	for name, dec := range map[string]func(types.Log) (string, map[string]any, error){
		"live":   BuildDecoder(addrs, ""),
		"strict": StrictDecoder(addrs, ""),
	} {
		t.Run(name, func(t *testing.T) {
			gotName, got, err := dec(purchase)
			if err != nil || gotName != "PurchaseRequested" || got["id"] != "3" || got["baseRound"] != "36893488147419103233" {
				t.Errorf("Vault PurchaseRequested: %q %v %v", gotName, got, err)
			}
			gotName, got, err = dec(redemption)
			if err != nil || gotName != "RedemptionPriced" || got["id"] != "4" || got["roundId"] != "36893488147419103233" || got["quoteAmount"] != "950" {
				t.Errorf("escrow RedemptionPriced: %q %v %v", gotName, got, err)
			}
		})
	}

	// the same topic from an address that is not the Vault is not a purchase order
	other := purchase
	other.Address = common.HexToAddress(addrs.Compliance)
	if gotName, _, _ := BuildDecoder(addrs, "")(other); gotName == "PurchaseRequested" {
		t.Error("a PurchaseRequested topic from the compliance registry was typed as an order")
	}
}

func TestDecodeProjectDeployedForRwaV4KeepsTheIncomeDistributor(t *testing.T) {
	f := bindings.NewFactory()
	addr := func(n byte) common.Address { return common.BytesToAddress([]byte{0xC0, n}) }
	factory := common.HexToAddress("0x00000000000000000000000000000000000FACE0")
	ev := f.ABI.Events["ProjectDeployed"]
	data, err := ev.Inputs.NonIndexed().Pack(addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), addr(7), "rwa-v4")
	if err != nil {
		t.Fatal(err)
	}
	name, got, err := BuildDecoder(models.Addresses{}, factory.Hex())(types.Log{
		Address: factory,
		Data:    data,
		Topics:  []common.Hash{ev.ID, common.HexToHash("0x01"), common.HexToHash("0x02")},
	})
	if err != nil || name != "ProjectDeployed" {
		t.Fatalf("%q %v", name, err)
	}
	if got["incomeDistributor"] != addr(7).Hex() || got["version"] != "rwa-v4" || got["strategy"] != addr(6).Hex() {
		t.Errorf("v4 data: %v", got)
	}
}

// Each child contract reaches the governance decoder after its own, and DLQ retry accepts
// only known names
func TestAdminDelayEvents_DecodeOnEveryChildContract(t *testing.T) {
	addrs := v3Addrs()
	g := bindings.NewGovernance()
	strict := StrictDecoder(addrs, "")
	for _, child := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy, addrs.Token, addrs.IncomeDistributor} {
		log := types.Log{Address: common.HexToAddress(child), Topics: []common.Hash{g.EventID("DefaultAdminDelayChangeCanceled")}}
		if name, _, err := strict(log); err != nil || name != "DefaultAdminDelayChangeCanceled" {
			t.Errorf("%s: decoded %q, err %v", child, name, err)
		}
	}
	if known := KnownEventNames(addrs, ""); !known["DefaultAdminDelayChangeScheduled"] {
		t.Error("DefaultAdminDelayChangeScheduled is not a known event")
	}
}
