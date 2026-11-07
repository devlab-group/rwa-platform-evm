package governance

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

func pausedLog(t *testing.T, name string, account common.Address) types.Log {
	t.Helper()
	g := bindings.NewGovernance()
	data, err := g.ABI.Events[name].Inputs.NonIndexed().Pack(account)
	if err != nil {
		t.Fatal(err)
	}
	return types.Log{Topics: []common.Hash{g.EventID(name)}, Data: data}
}

func roleLog(name string, role common.Hash, account, sender common.Address) types.Log {
	g := bindings.NewGovernance()
	return types.Log{
		Topics: []common.Hash{
			g.EventID(name),
			role,
			common.BytesToHash(account.Bytes()),
			common.BytesToHash(sender.Bytes()),
		},
	}
}

func TestDecodePausedUnpaused(t *testing.T) {
	acct := common.HexToAddress("0x00000000000000000000000000000000000000A1")
	for _, name := range []string{"Paused", "Unpaused"} {
		gotName, data, err := DecodeLog(pausedLog(t, name, acct))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if gotName != name {
			t.Fatalf("name = %q, want %q", gotName, name)
		}
		if data["account"] != acct.Hex() {
			t.Fatalf("%s account = %v, want %s", name, data["account"], acct.Hex())
		}
	}
}

func TestDecodeRoleGrantedRevoked(t *testing.T) {
	role := common.Hash(bindings.PauserRole)
	acct := common.HexToAddress("0x00000000000000000000000000000000000000B2")
	sender := common.HexToAddress("0x00000000000000000000000000000000000000C3")
	for _, name := range []string{"RoleGranted", "RoleRevoked"} {
		gotName, data, err := DecodeLog(roleLog(name, role, acct, sender))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if gotName != name {
			t.Fatalf("name = %q, want %q", gotName, name)
		}
		if data["role"] != role.Hex() || data["account"] != acct.Hex() || data["sender"] != sender.Hex() {
			t.Fatalf("%s fields = %v", name, data)
		}
	}
}

func TestDecodeDefaultAdminTransferScheduled(t *testing.T) {
	g := bindings.NewGovernance()
	newAdmin := common.HexToAddress("0x00000000000000000000000000000000000000D4")
	log := types.Log{
		Topics: []common.Hash{
			g.EventID("DefaultAdminTransferScheduled"),
			common.BytesToHash(newAdmin.Bytes()), // newAdmin is indexed in topic1
		},
	}
	name, data, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "DefaultAdminTransferScheduled" {
		t.Fatalf("name = %q, want DefaultAdminTransferScheduled", name)
	}
	if data["newAdmin"] != newAdmin.Hex() {
		t.Fatalf("newAdmin = %v, want %s", data["newAdmin"], newAdmin.Hex())
	}
}

func TestDecodeDefaultAdminTransferCanceled(t *testing.T) {
	g := bindings.NewGovernance()
	log := types.Log{Topics: []common.Hash{g.EventID("DefaultAdminTransferCanceled")}}
	name, data, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "DefaultAdminTransferCanceled" {
		t.Fatalf("name = %q, want DefaultAdminTransferCanceled", name)
	}
	if len(data) != 0 {
		t.Fatalf("data = %v, want empty", data)
	}
}

func TestDecodeDefaultAdminDelayChangeScheduled(t *testing.T) {
	g := bindings.NewGovernance()
	// 86400 and 1800000000 as two 32-byte words, the layout OpenZeppelin emits
	data := common.FromHex("0x" +
		"0000000000000000000000000000000000000000000000000000000000015180" +
		"000000000000000000000000000000000000000000000000000000006b49d200")
	name, got, err := DecodeLog(types.Log{Topics: []common.Hash{g.EventID("DefaultAdminDelayChangeScheduled")}, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if name != "DefaultAdminDelayChangeScheduled" {
		t.Fatalf("name = %q, want DefaultAdminDelayChangeScheduled", name)
	}
	if got["newDelay"] != "86400" || got["effectSchedule"] != "1800000000" {
		t.Fatalf("data = %v, want newDelay 86400 and effectSchedule 1800000000", got)
	}
}

func TestDecodeDefaultAdminDelayChangeScheduled_TruncatedData_IsAnError(t *testing.T) {
	g := bindings.NewGovernance()
	log := types.Log{Topics: []common.Hash{g.EventID("DefaultAdminDelayChangeScheduled")}, Data: make([]byte, 31)}
	if name, _, err := DecodeLog(log); err == nil {
		t.Fatalf("decoded %q from truncated data", name)
	}
}

func TestDecodeDefaultAdminDelayChangeCanceled(t *testing.T) {
	g := bindings.NewGovernance()
	name, data, err := DecodeLog(types.Log{Topics: []common.Hash{g.EventID("DefaultAdminDelayChangeCanceled")}})
	if err != nil {
		t.Fatal(err)
	}
	if name != "DefaultAdminDelayChangeCanceled" || len(data) != 0 {
		t.Fatalf("name = %q, data = %v, want DefaultAdminDelayChangeCanceled and no fields", name, data)
	}
}

func TestDecodeUnknownTopicFallsThrough(t *testing.T) {
	log := types.Log{Topics: []common.Hash{common.HexToHash("0xdeadbeef")}}
	name, _, err := DecodeLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if name != "unknown" {
		t.Fatalf("name = %q, want unknown", name)
	}
}
