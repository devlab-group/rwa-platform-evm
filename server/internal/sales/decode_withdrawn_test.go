package sales

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func TestDecodeProceedsWithdrawn(t *testing.T) {
	v := bindings.NewVault()
	treasury := common.HexToAddress("0x0000000000000000000000000000000000000044")
	caller := common.HexToAddress("0x0000000000000000000000000000000000000055")
	amount := big.NewInt(123456)

	// treasury + caller are indexed (topics 1,2, declaration order); quoteAmount
	// is the only non-indexed field, so it lives in log.Data.
	data, err := v.ABI.Events["ProceedsWithdrawn"].Inputs.NonIndexed().Pack(amount)
	if err != nil {
		t.Fatal(err)
	}
	log := types.Log{
		Topics: []common.Hash{
			v.EventID("ProceedsWithdrawn"),
			common.BytesToHash(treasury.Bytes()), common.BytesToHash(caller.Bytes()),
		},
		Data: data,
	}

	name, got, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "ProceedsWithdrawn" {
		t.Fatalf("name = %q, want ProceedsWithdrawn", name)
	}
	if got["treasury"] != treasury.Hex() || got["caller"] != caller.Hex() || got["quoteAmount"] != "123456" {
		t.Fatalf("fields = %v", got)
	}
}
