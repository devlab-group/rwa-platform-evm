package redemption

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
)

func TestDecodeLogTypesEveryRedemptionEvent(t *testing.T) {
	e := bindings.NewRedemptionEscrow()
	idTopic := common.BigToHash(big.NewInt(42))
	who := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	whoTopic := common.BytesToHash(who.Bytes())
	reason := common.HexToHash("0xdead")

	pack := func(name string, vals ...any) []byte {
		out, err := e.ABI.Events[name].Inputs.NonIndexed().Pack(vals...)
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
			"RedemptionRequested",
			types.Log{Topics: []common.Hash{e.EventID("RedemptionRequested"), idTopic, whoTopic},
				Data: pack("RedemptionRequested", big.NewInt(1000), big.NewInt(950), uint64(1700000000))},
			map[string]any{"id": "42", "beneficiary": who.Hex(), "rwaAmount": "1000", "quoteAmount": "950", "createdAt": uint64(1700000000)},
		},
		{
			"RedemptionFunded",
			types.Log{Topics: []common.Hash{e.EventID("RedemptionFunded"), idTopic, whoTopic}, Data: pack("RedemptionFunded", big.NewInt(950))},
			map[string]any{"id": "42", "funder": who.Hex(), "quoteAmount": "950"},
		},
		{
			"RedemptionCompleted",
			types.Log{Topics: []common.Hash{e.EventID("RedemptionCompleted"), idTopic, whoTopic},
				Data: pack("RedemptionCompleted", big.NewInt(1000), big.NewInt(950))},
			map[string]any{"id": "42", "beneficiary": who.Hex(), "rwaAmount": "1000", "quoteAmount": "950"},
		},
		{
			"RedemptionRejected",
			types.Log{Topics: []common.Hash{e.EventID("RedemptionRejected"), idTopic, reason, whoTopic}},
			map[string]any{"id": "42", "reasonCode": reason.Hex(), "caller": who.Hex()},
		},
		{
			"RedemptionCancelled",
			types.Log{Topics: []common.Hash{e.EventID("RedemptionCancelled"), idTopic, whoTopic}},
			map[string]any{"id": "42", "beneficiary": who.Hex()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, data, err := DecodeLog(tc.log)
			if err != nil {
				t.Fatal(err)
			}
			if name != tc.name {
				t.Fatalf("name = %q, want %q", name, tc.name)
			}
			for k, want := range tc.want {
				if data[k] != want {
					t.Errorf("%s = %v (%T), want %v (%T)", k, data[k], data[k], want, want)
				}
			}
			if len(data) != len(tc.want) {
				t.Errorf("data has %d keys, want %d: %v", len(data), len(tc.want), data)
			}
		})
	}
}

func TestDecodeLogLeavesOtherLogsToTheFallback(t *testing.T) {
	foreign := common.HexToHash("0xfeed")
	name, data, err := DecodeLog(types.Log{Topics: []common.Hash{foreign}})
	if err != nil || name != "unknown" || data["topic0"] != foreign.Hex() {
		t.Errorf("foreign topic: got (%q, %v, %v), want unknown with topic0 %s", name, data, err, foreign.Hex())
	}
	name, data, err = DecodeLog(types.Log{})
	if err != nil || name != "unknown" || data != nil {
		t.Errorf("no topics: got (%q, %v, %v), want unknown with no data", name, data, err)
	}
}

func TestDecodeLogRejectsTruncatedData(t *testing.T) {
	e := bindings.NewRedemptionEscrow()
	topics := []common.Hash{e.EventID("RedemptionRequested"), common.BigToHash(big.NewInt(1)), common.BigToHash(big.NewInt(2))}
	name, data, err := DecodeLog(types.Log{Topics: topics, Data: []byte{0x01, 0x02}})
	if err == nil {
		t.Fatalf("got (%q, %v, nil), want a decode error for truncated data", name, data)
	}
}

// Supported createdAt encodings must yield the same timeout; unusable values read as zero
func TestBuildStates_CreatedAtNumericEncodings(t *testing.T) {
	tests := []struct {
		name      string
		createdAt any
		want      int64
	}{
		{"uint64 from the decoder", uint64(1700000000), 1700000000},
		{"int64 from mongo", int64(1700000000), 1700000000},
		{"int32 from mongo", int32(1700000000), 1700000000},
		{"int", 1700000000, 1700000000},
		{"float64 from json", float64(1700000000), 1700000000},
		{"decimal string", "1700000000", 1700000000},
		{"garbage string", "soon", 0},
		{"unsupported type", true, 0},
		{"missing", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			states := BuildStates([]*models.ChainEvent{
				ev("RedemptionRequested", "1", 1, 0, map[string]any{"createdAt": tt.createdAt}),
			}, 100)
			r := states["1"]
			if r.CreatedAt != tt.want || r.TimeoutAt != tt.want+100 {
				t.Errorf("CreatedAt/TimeoutAt = %d/%d, want %d/%d", r.CreatedAt, r.TimeoutAt, tt.want, tt.want+100)
			}
		})
	}
}
