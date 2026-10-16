package income

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func TestDecodeLogTypesEveryIncomeEvent(t *testing.T) {
	d := bindings.NewIncomeDistributor()
	id := big.NewInt(12)
	idTopic := common.BigToHash(id)
	acct := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	root := common.HexToHash("0xabcd")
	sha := common.HexToHash("0x1234")
	addrTopic := common.BytesToHash(acct.Bytes())

	pack := func(name string, vals ...any) []byte {
		out, err := d.ABI.Events[name].Inputs.NonIndexed().Pack(vals...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	cases := []struct {
		name string
		log  types.Log
		want map[string]any
	}{
		{
			"DistributionCreated",
			types.Log{Topics: []common.Hash{d.EventID("DistributionCreated"), idTopic, addrTopic},
				Data: pack("DistributionCreated", root, big.NewInt(1000), uint64(55), uint64(2000), "ipfs://cid", sha)},
			map[string]any{"id": "12", "merkleRoot": root.Hex(), "total": "1000", "recordBlock": "55", "claimDeadline": "2000",
				"snapshotURI": "ipfs://cid", "snapshotSha256": sha.Hex(), "caller": acct.Hex()},
		},
		{
			"IncomeClaimed",
			types.Log{Topics: []common.Hash{d.EventID("IncomeClaimed"), idTopic, addrTopic}, Data: pack("IncomeClaimed", big.NewInt(300))},
			map[string]any{"id": "12", "account": acct.Hex(), "amount": "300"},
		},
		{
			"UnclaimedReclaimed",
			types.Log{Topics: []common.Hash{d.EventID("UnclaimedReclaimed"), idTopic, addrTopic}, Data: pack("UnclaimedReclaimed", big.NewInt(9))},
			map[string]any{"id": "12", "treasury": acct.Hex(), "amount": "9"},
		},
		{
			"ClaimDeadlineExtended",
			types.Log{Topics: []common.Hash{d.EventID("ClaimDeadlineExtended"), idTopic, addrTopic}, Data: pack("ClaimDeadlineExtended", uint64(2000), uint64(3000))},
			map[string]any{"id": "12", "previousDeadline": "2000", "newDeadline": "3000", "caller": acct.Hex()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, data, err := DecodeLog(tc.log)
			if err != nil {
				t.Fatal(err)
			}
			if name != tc.name {
				t.Fatalf("name = %q", name)
			}
			for k, want := range tc.want {
				if data[k] != want {
					t.Errorf("%s = %v, want %v", k, data[k], want)
				}
			}
			if len(data) != len(tc.want) {
				t.Errorf("data has %d keys, want %d: %v", len(data), len(tc.want), data)
			}
		})
	}

	if name, _, err := DecodeLog(types.Log{Topics: []common.Hash{common.HexToHash("0x99")}}); err != nil || name != "unknown" {
		t.Errorf("foreign topic: %q %v", name, err)
	}
	if name, _, err := DecodeLog(types.Log{}); err != nil || name != "unknown" {
		t.Errorf("no topics: %q %v", name, err)
	}
	if _, _, err := DecodeLog(types.Log{Topics: []common.Hash{d.EventID("IncomeClaimed")}}); err == nil {
		t.Error("a malformed IncomeClaimed log decoded without error")
	}
}
