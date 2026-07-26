package assets

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// makeLog builds a minimal types.Log for decoder tests: topics[0] is the
// event ID, followed by any indexed topics, with data as the packed
// non-indexed fields.
func makeLog(eventID common.Hash, indexedTopics []common.Hash, data []byte) types.Log {
	topics := append([]common.Hash{eventID}, indexedTopics...)
	return types.Log{Topics: topics, Data: data}
}

func TestDecodeLogMinted(t *testing.T) {
	controller := bindings.NewSupplyController()
	event := controller.ABI.Events["Minted"]
	auditor := common.HexToAddress("0x0000000000000000000000000000000000AAAA")
	packed, err := event.Inputs.NonIndexed().Pack(big.NewInt(1000), big.NewInt(7), auditor)
	if err != nil {
		t.Fatal(err)
	}
	recordKey := common.HexToHash("0x01")
	metadataDigest := common.HexToHash("0x02")
	vault := common.HexToAddress("0x0000000000000000000000000000000000BBBB")
	log := makeLog(event.ID, []common.Hash{recordKey, metadataDigest, common.BytesToHash(vault.Bytes())}, packed)

	name, data, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Minted" {
		t.Fatalf("name = %q, want Minted", name)
	}
	if data["recordKey"] != recordKey.Hex() {
		t.Errorf("recordKey = %v", data["recordKey"])
	}
	if data["amount"] != "1000" {
		t.Errorf("amount = %v", data["amount"])
	}
}

func TestDecodeLogUnknownTopic(t *testing.T) {
	log := makeLog(common.HexToHash("0xdeadbeef"), nil, nil)
	name, _, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "unknown" {
		t.Errorf("name = %q, want unknown", name)
	}
}
