package serverwiring

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
)

func v3Addrs() models.Addresses {
	a := func(n byte) string { return common.BytesToAddress([]byte{0xB0, n}).Hex() }
	return models.Addresses{
		Token: a(1), Compliance: a(2), SupplyController: a(3), Vault: a(4), RedemptionEscrow: a(5), Strategy: a(6),
		IncomeDistributor: a(7),
	}
}

func TestIndexerAddressesIncludesTheIncomeDistributor(t *testing.T) {
	addrs := v3Addrs()
	got := IndexerAddresses(addrs, "0x00000000000000000000000000000000000FACE0")
	want := map[common.Address]bool{common.HexToAddress(addrs.IncomeDistributor): false}
	for _, a := range got {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	if !want[common.HexToAddress(addrs.IncomeDistributor)] {
		t.Errorf("income distributor missing from %v", got)
	}
	if len(got) != 8 {
		t.Errorf("a v3 stack plus the factory is 8 addresses, got %d", len(got))
	}

	addrs.IncomeDistributor = ""
	if got := IndexerAddresses(addrs, ""); len(got) != 6 {
		t.Errorf("an rwa-v2 stack is 6 addresses, got %d", len(got))
	}
}

func TestBuildDecoderRoutesIncomeAndTransferLogs(t *testing.T) {
	addrs := v3Addrs()
	dec := BuildDecoder(addrs, "")

	d := bindings.NewIncomeDistributor()
	data, err := d.ABI.Events["IncomeClaimed"].Inputs.NonIndexed().Pack(big.NewInt(5))
	if err != nil {
		t.Fatal(err)
	}
	acct := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	name, got, err := dec(types.Log{
		Address: common.HexToAddress(addrs.IncomeDistributor), Data: data,
		Topics: []common.Hash{d.EventID("IncomeClaimed"), common.BigToHash(big.NewInt(3)), common.BytesToHash(acct.Bytes())},
	})
	if err != nil || name != "IncomeClaimed" || got["amount"] != "5" {
		t.Errorf("income log: %q %v %v", name, got, err)
	}

	// the distributor is an AccessControl contract, so governance events decode there too
	g := bindings.NewGovernance()
	name, _, err = dec(types.Log{
		Address: common.HexToAddress(addrs.IncomeDistributor),
		Topics:  []common.Hash{g.EventID("RoleGranted"), common.Hash(bindings.IncomeManagerRole), common.BytesToHash(acct.Bytes()), common.BytesToHash(acct.Bytes())},
	})
	if err != nil || name != "RoleGranted" {
		t.Errorf("role log on the distributor: %q %v", name, err)
	}

	erc20 := bindings.NewERC20()
	tdata, err := erc20.ABI.Events["Transfer"].Inputs.NonIndexed().Pack(big.NewInt(7))
	if err != nil {
		t.Fatal(err)
	}
	name, got, err = dec(types.Log{
		Address: common.HexToAddress(addrs.Token), Data: tdata,
		Topics: []common.Hash{erc20.ABI.Events["Transfer"].ID, common.BytesToHash(acct.Bytes()), common.BytesToHash(acct.Bytes())},
	})
	if err != nil || name != "Transfer" || got["value"] != "7" {
		t.Errorf("token Transfer: %q %v %v", name, got, err)
	}
}

func TestKnownEventNamesCoverIncomeAndTransfer(t *testing.T) {
	known := KnownEventNames(v3Addrs(), "")
	for _, n := range []string{"DistributionCreated", "IncomeClaimed", "UnclaimedReclaimed", "ClaimDeadlineExtended", "Transfer", "MinPurchaseQuoteChanged", "RoleGranted"} {
		if !known[n] {
			t.Errorf("%s is not a known event for a v3 stack", n)
		}
	}
	v2 := v3Addrs()
	v2.IncomeDistributor = ""
	if known := KnownEventNames(v2, ""); known["DistributionCreated"] {
		t.Error("an rwa-v2 stack must not claim income events")
	}
}

func TestDecodeProjectDeployedBothGenerations(t *testing.T) {
	f := bindings.NewFactory()
	addr := func(n byte) common.Address { return common.BytesToAddress([]byte{0xC0, n}) }
	pid, pd := common.HexToHash("0x01"), common.HexToHash("0x02")
	dec := BuildDecoder(models.Addresses{}, "0x00000000000000000000000000000000000FACE0")
	factory := common.HexToAddress("0x00000000000000000000000000000000000FACE0")

	ev3 := f.ABI.Events["ProjectDeployed"]
	d3, err := ev3.Inputs.NonIndexed().Pack(addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), addr(7), "rwa-v3")
	if err != nil {
		t.Fatal(err)
	}
	name, got, err := dec(types.Log{Address: factory, Data: d3, Topics: []common.Hash{ev3.ID, pid, pd}})
	if err != nil || name != "ProjectDeployed" {
		t.Fatalf("v3: %q %v", name, err)
	}
	if got["incomeDistributor"] != addr(7).Hex() || got["strategy"] != addr(6).Hex() || got["version"] != "rwa-v3" {
		t.Errorf("v3 data: %v", got)
	}

	ev2 := f.ABIV2.Events["ProjectDeployed"]
	d2, err := ev2.Inputs.NonIndexed().Pack(addr(1), addr(2), addr(3), addr(4), addr(5), addr(6), "rwa-v2")
	if err != nil {
		t.Fatal(err)
	}
	name, got, err = dec(types.Log{Address: factory, Data: d2, Topics: []common.Hash{ev2.ID, pid, pd}})
	if err != nil || name != "ProjectDeployed" {
		t.Fatalf("v2: %q %v", name, err)
	}
	if _, has := got["incomeDistributor"]; has || got["version"] != "rwa-v2" || got["strategy"] != addr(6).Hex() {
		t.Errorf("v2 data: %v", got)
	}
}
