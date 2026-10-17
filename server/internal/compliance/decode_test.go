package compliance

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func TestDecodeLogStatusChanged(t *testing.T) {
	registry := bindings.NewComplianceRegistry()
	event := registry.ABI.Events["StatusChanged"]
	packed, err := event.Inputs.NonIndexed().Pack(uint8(1), uint8(2), uint64(0), uint64(999))
	if err != nil {
		t.Fatal(err)
	}
	account := common.HexToAddress("0x0000000000000000000000000000000000AAAA")
	caller := common.HexToAddress("0x0000000000000000000000000000000000BBBB")
	log := types.Log{
		Topics: []common.Hash{event.ID, common.BytesToHash(account.Bytes()), common.BytesToHash(caller.Bytes())},
		Data:   packed,
	}

	name, data, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "StatusChanged" {
		t.Fatalf("name = %q, want StatusChanged", name)
	}
	want := map[string]any{
		"account": account.Hex(), "caller": caller.Hex(),
		"previousStatus": uint8(1), "newStatus": uint8(2),
		"previousValidUntil": uint64(0), "newValidUntil": uint64(999),
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("%s = %v (%T), want %v (%T)", k, data[k], data[k], v, v)
		}
	}
	if len(data) != len(want) {
		t.Errorf("data has %d keys, want %d: %v", len(data), len(want), data)
	}
}

func TestDecodeLogStatusChangedRejectsTruncatedData(t *testing.T) {
	event := bindings.NewComplianceRegistry().ABI.Events["StatusChanged"]
	log := types.Log{Topics: []common.Hash{event.ID, {}, {}}, Data: []byte{1}}
	if _, _, err := DecodeLog(log); err == nil {
		t.Fatal("DecodeLog accepted a truncated StatusChanged log")
	}
}

func TestDecodeLogUnknownTopic(t *testing.T) {
	log := types.Log{Topics: []common.Hash{common.HexToHash("0xdeadbeef")}}
	name, _, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "unknown" {
		t.Errorf("name = %q, want unknown", name)
	}
}
