package assets

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func TestDecodeLogSupplyEventsMapEveryField(t *testing.T) {
	c := bindings.NewSupplyController()
	key := common.HexToHash("0x1111")
	digest := common.HexToHash("0x2222")
	vault := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	auditor := common.HexToAddress("0x00000000000000000000000000000000000000a2")
	amount, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	nonce, _ := new(big.Int).SetString("98765432109876543210", 10)
	prev := common.HexToAddress("0x00000000000000000000000000000000000000e1")
	next := common.HexToAddress("0x00000000000000000000000000000000000000e2")
	caller := common.HexToAddress("0x00000000000000000000000000000000000000e3")

	pack := func(name string) []byte {
		out, err := c.ABI.Events[name].Inputs.NonIndexed().Pack(amount, nonce, auditor)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	addrTopic := func(a common.Address) common.Hash { return common.BytesToHash(a.Bytes()) }
	tests := []struct {
		event string
		log   types.Log
		want  map[string]any
	}{
		{"Minted", types.Log{Topics: []common.Hash{c.EventID("Minted"), key, digest, addrTopic(vault)}, Data: pack("Minted")},
			map[string]any{"recordKey": key.Hex(), "metadataDigest": digest.Hex(), "vault": vault.Hex(),
				"amount": amount.String(), "nonce": nonce.String(), "auditor": auditor.Hex()}},
		{"Burned", types.Log{Topics: []common.Hash{c.EventID("Burned"), key, digest, addrTopic(vault)}, Data: pack("Burned")},
			map[string]any{"operationId": key.Hex(), "metadataDigest": digest.Hex(), "vault": vault.Hex(),
				"amount": amount.String(), "nonce": nonce.String(), "auditor": auditor.Hex()}},
		{"AuditorChanged", types.Log{Topics: []common.Hash{c.EventID("AuditorChanged"), addrTopic(prev), addrTopic(next), addrTopic(caller)}},
			map[string]any{"previousAuditor": prev.Hex(), "newAuditor": next.Hex(), "caller": caller.Hex()}},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			name, data, err := DecodeLog(tt.log)
			if err != nil {
				t.Fatal(err)
			}
			if name != tt.event {
				t.Fatalf("name = %q, want %q", name, tt.event)
			}
			for k, v := range tt.want {
				if data[k] != v {
					t.Errorf("%s = %v, want %v", k, data[k], v)
				}
			}
			if len(data) != len(tt.want) {
				t.Errorf("data has %d keys, want %d: %v", len(data), len(tt.want), data)
			}
		})
	}
}

func TestDecodeLogSupplyEventsRejectTruncatedData(t *testing.T) {
	c := bindings.NewSupplyController()
	for _, name := range []string{"Minted", "Burned"} {
		log := types.Log{Topics: []common.Hash{c.EventID(name), {}, {}, {}}, Data: []byte{1}}
		if _, _, err := DecodeLog(log); err == nil {
			t.Errorf("%s: DecodeLog accepted truncated data", name)
		}
	}
}
