package strategy

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func priceLog(t *testing.T, name string, prev, next *big.Int, caller common.Address) types.Log {
	t.Helper()
	s := bindings.NewFixedPriceStrategy()
	data, err := s.ABI.Events[name].Inputs.NonIndexed().Pack(prev, next)
	if err != nil {
		t.Fatal(err)
	}
	return types.Log{
		Topics: []common.Hash{s.EventID(name), common.BytesToHash(caller.Bytes())},
		Data:   data,
	}
}

func TestDecodePriceUpdated(t *testing.T) {
	caller := common.HexToAddress("0x00000000000000000000000000000000000000D4")
	for _, name := range []string{"PurchasePriceUpdated", "RedemptionPriceUpdated"} {
		gotName, data, err := DecodeLog(priceLog(t, name, big.NewInt(100), big.NewInt(250), caller))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if gotName != name {
			t.Fatalf("name = %q, want %q", gotName, name)
		}
		if data["previousPrice"] != "100" || data["newPrice"] != "250" || data["caller"] != caller.Hex() {
			t.Fatalf("%s fields = %v", name, data)
		}
	}
}
