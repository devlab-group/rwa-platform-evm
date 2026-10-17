package sales

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// Distinct field values and amounts above 2^64 expose swaps and truncation
func TestDecodeLogPurchasedMapsEveryField(t *testing.T) {
	vault := bindings.NewVault()
	event := vault.ABI.Events["Purchased"]
	buyer := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	recipient := common.HexToAddress("0x00000000000000000000000000000000000000c2")
	strategy := common.HexToAddress("0x00000000000000000000000000000000000000d3")
	tokens, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	quote, _ := new(big.Int).SetString("98765432109876543210", 10)
	packed, err := event.Inputs.NonIndexed().Pack(tokens, quote, strategy)
	if err != nil {
		t.Fatal(err)
	}
	log := types.Log{
		Topics: []common.Hash{event.ID, common.BytesToHash(buyer.Bytes()), common.BytesToHash(recipient.Bytes())},
		Data:   packed,
	}

	name, data, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Purchased" {
		t.Fatalf("name = %q, want Purchased", name)
	}
	want := map[string]any{
		"buyer": buyer.Hex(), "recipient": recipient.Hex(), "strategy": strategy.Hex(),
		"tokenAmount": "123456789012345678901234567890", "quoteAmount": "98765432109876543210",
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("%s = %v, want %v", k, data[k], v)
		}
	}
	if len(data) != len(want) {
		t.Errorf("data has %d keys, want %d: %v", len(data), len(want), data)
	}
}

func TestDecodeLogPurchasedRejectsTruncatedData(t *testing.T) {
	event := bindings.NewVault().ABI.Events["Purchased"]
	log := types.Log{Topics: []common.Hash{event.ID, {}, {}}, Data: []byte{1, 2}}
	if _, _, err := DecodeLog(log); err == nil {
		t.Fatal("DecodeLog accepted a truncated Purchased log")
	}
}
