package sales

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func addrChangeLog(name string, prev, next, caller common.Address) types.Log {
	v := bindings.NewVault()
	return types.Log{Topics: []common.Hash{
		v.EventID(name),
		common.BytesToHash(prev.Bytes()), common.BytesToHash(next.Bytes()), common.BytesToHash(caller.Bytes()),
	}}
}

func TestDecodeTreasuryStrategyChanged(t *testing.T) {
	prev := common.HexToAddress("0x0000000000000000000000000000000000000011")
	next := common.HexToAddress("0x0000000000000000000000000000000000000022")
	caller := common.HexToAddress("0x0000000000000000000000000000000000000033")

	cases := []struct{ name, prevKey, newKey string }{
		{"TreasuryChanged", "previousTreasury", "newTreasury"},
		{"StrategyChanged", "previousStrategy", "newStrategy"},
	}
	for _, c := range cases {
		name, data, err := DecodeLog(addrChangeLog(c.name, prev, next, caller))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if name != c.name {
			t.Fatalf("name = %q, want %q", name, c.name)
		}
		if data[c.prevKey] != prev.Hex() || data[c.newKey] != next.Hex() || data["caller"] != caller.Hex() {
			t.Fatalf("%s fields = %v", c.name, data)
		}
	}
}

func TestDecodeMinPurchaseQuoteChanged(t *testing.T) {
	v := bindings.NewVault()
	caller := common.HexToAddress("0x00000000000000000000000000000000000000C1")
	data, err := v.ABI.Events["MinPurchaseQuoteChanged"].Inputs.NonIndexed().Pack(big.NewInt(5), big.NewInt(50))
	if err != nil {
		t.Fatal(err)
	}
	name, got, err := DecodeLog(types.Log{
		Topics: []common.Hash{v.EventID("MinPurchaseQuoteChanged"), common.BytesToHash(caller.Bytes())}, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if name != "MinPurchaseQuoteChanged" || got["previousMinimum"] != "5" || got["newMinimum"] != "50" || got["caller"] != caller.Hex() {
		t.Errorf("decoded %q %+v", name, got)
	}
}
