package token

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func frozenLog(t *testing.T, account common.Address, amount *big.Int) types.Log {
	t.Helper()
	tok := bindings.NewRWAToken()
	data, err := tok.ABI.Events["Frozen"].Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatal(err)
	}
	return types.Log{
		Topics: []common.Hash{tok.EventID("Frozen"), common.BytesToHash(account.Bytes())},
		Data:   data,
	}
}

func forcedTransferLog(t *testing.T, from, to common.Address, amount *big.Int) types.Log {
	t.Helper()
	tok := bindings.NewRWAToken()
	data, err := tok.ABI.Events["ForcedTransfer"].Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatal(err)
	}
	return types.Log{
		Topics: []common.Hash{
			tok.EventID("ForcedTransfer"),
			common.BytesToHash(from.Bytes()),
			common.BytesToHash(to.Bytes()),
		},
		Data: data,
	}
}

func TestDecodeFrozen(t *testing.T) {
	account := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	// Well past 2^64, to prove the amount never round-trips through a float or
	// a fixed-width int on its way to the projection.
	amount, _ := new(big.Int).SetString("123456789012345678901234567890", 10)

	name, data, err := DecodeLog(frozenLog(t, account, amount))
	if err != nil {
		t.Fatal(err)
	}
	if name != "Frozen" {
		t.Fatalf("name = %q, want Frozen", name)
	}
	if data["account"] != account.Hex() {
		t.Errorf("account = %v, want %s", data["account"], account.Hex())
	}
	if data["amount"] != amount.String() {
		t.Errorf("amount = %v, want %s", data["amount"], amount.String())
	}
}

func TestDecodeForcedTransfer(t *testing.T) {
	from := common.HexToAddress("0x00000000000000000000000000000000000000b2")
	to := common.HexToAddress("0x00000000000000000000000000000000000000c3")

	name, data, err := DecodeLog(forcedTransferLog(t, from, to, big.NewInt(42)))
	if err != nil {
		t.Fatal(err)
	}
	if name != "ForcedTransfer" {
		t.Fatalf("name = %q, want ForcedTransfer", name)
	}
	if data["from"] != from.Hex() || data["to"] != to.Hex() || data["amount"] != "42" {
		t.Errorf("decoded = %+v", data)
	}
}

// A log whose indexed topics are missing must fail rather than decode into the
// zero address, which would project a freeze against nobody.
func TestDecodeFailsClosedOnMissingTopics(t *testing.T) {
	full := frozenLog(t, common.HexToAddress("0xa1"), big.NewInt(1))

	if _, _, err := DecodeLog(types.Log{Topics: full.Topics[:1], Data: full.Data}); err == nil {
		t.Error("Frozen log without its account topic should not decode")
	}

	forced := forcedTransferLog(t, common.HexToAddress("0xb2"), common.HexToAddress("0xc3"), big.NewInt(1))
	if _, _, err := DecodeLog(types.Log{Topics: forced.Topics[:2], Data: forced.Data}); err == nil {
		t.Error("ForcedTransfer log without its recipient topic should not decode")
	}

	// Truncated data is a decode error too, not a zero amount.
	if _, _, err := DecodeLog(types.Log{Topics: full.Topics, Data: full.Data[:16]}); err == nil {
		t.Error("Frozen log with truncated data should not decode")
	}

	// An unrelated topic0 is not an error: it falls through to the shared
	// governance decoder, the same as every other contract-specific decoder.
	unrelated := bindings.NewGovernance().EventID("Paused")
	name, _, err := DecodeLog(types.Log{Topics: []common.Hash{unrelated}, Data: nil})
	if err != nil || name != "unknown" {
		t.Errorf("unrelated topic0: name=%q err=%v, want unknown/nil", name, err)
	}
	if name, _, err := DecodeLog(types.Log{}); err != nil || name != "unknown" {
		t.Errorf("topicless log: name=%q err=%v, want unknown/nil", name, err)
	}
}
