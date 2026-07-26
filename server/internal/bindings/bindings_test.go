package bindings

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func selector(sig string) []byte {
	return crypto.Keccak256([]byte(sig))[:4]
}

func TestComplianceRegistrySetStatusSelectorAndDecode(t *testing.T) {
	c := NewComplianceRegistry()
	acct := common.HexToAddress("0x000000000000000000000000000000000000A1")
	data, err := c.PackSetStatus(acct, ComplianceStatusAllowed, 12345)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:4], selector("setStatus(address,uint8,uint64)")) {
		t.Errorf("unexpected selector: %x", data[:4])
	}

	method, err := c.ABI.MethodById(data[:4])
	if err != nil {
		t.Fatal(err)
	}
	args, err := method.Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatal(err)
	}
	if args[0].(common.Address) != acct {
		t.Errorf("account mismatch")
	}
	if args[1].(uint8) != uint8(ComplianceStatusAllowed) {
		t.Errorf("status mismatch")
	}
	if args[2].(uint64) != 12345 {
		t.Errorf("validUntil mismatch")
	}
}

func TestComplianceRegistryEventRoundTrip(t *testing.T) {
	c := NewComplianceRegistry()
	event := c.ABI.Events["StatusChanged"]
	packed, err := event.Inputs.NonIndexed().Pack(uint8(1), uint8(2), uint64(0), uint64(999))
	// NonIndexed fields for StatusChanged: previousStatus, newStatus, previousValidUntil, newValidUntil (account/caller are indexed).
	if err != nil {
		t.Fatal(err)
	}
	account := common.HexToAddress("0x00000000000000000000000000000000000B2")
	caller := common.HexToAddress("0x00000000000000000000000000000000000C3")
	topics := []common.Hash{event.ID, common.BytesToHash(account.Bytes()), common.BytesToHash(caller.Bytes())}

	ev, err := c.UnpackStatusChanged(packed, topics)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Account != account || ev.Caller != caller {
		t.Errorf("indexed fields mismatch: %+v", ev)
	}
	if ev.PreviousStatus != 1 || ev.NewStatus != 2 || ev.NewValidUntil != 999 {
		t.Errorf("non-indexed fields mismatch: %+v", ev)
	}
}

func TestSupplyControllerMintPackUnpack(t *testing.T) {
	s := NewSupplyController()
	m := MintAttestation{
		Auditor:        common.HexToAddress("0x00000000000000000000000000000000000001"),
		ProfileDigest:  [32]byte{1, 2, 3},
		RecordKey:      [32]byte{4, 5, 6},
		MetadataDigest: [32]byte{7, 8, 9},
		Amount:         big.NewInt(1000),
		Nonce:          big.NewInt(42),
		ValidUntil:     1800000000,
		Vault:          common.HexToAddress("0x00000000000000000000000000000000000002"),
	}
	sig := bytes.Repeat([]byte{0xAB}, 65)

	data, err := s.PackMint(m, sig)
	if err != nil {
		t.Fatalf("PackMint: %v", err)
	}
	if !bytes.Equal(data[:4], s.ABI.Methods["mint"].ID) {
		t.Errorf("selector mismatch")
	}

	args, err := s.ABI.Methods["mint"].Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	got := abi.ConvertType(args[0], new(MintAttestation)).(*MintAttestation)
	if got.Auditor != m.Auditor || got.Amount.Cmp(m.Amount) != 0 || got.Nonce.Cmp(m.Nonce) != 0 || got.ValidUntil != m.ValidUntil || got.Vault != m.Vault {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, m)
	}
	if got.ProfileDigest != m.ProfileDigest || got.RecordKey != m.RecordKey || got.MetadataDigest != m.MetadataDigest {
		t.Errorf("digest fields mismatch")
	}
	gotSig := args[1].([]byte)
	if !bytes.Equal(gotSig, sig) {
		t.Errorf("signature mismatch")
	}
}

func TestSupplyControllerMintedEventDecode(t *testing.T) {
	s := NewSupplyController()
	event := s.ABI.Events["Minted"]
	auditor := common.HexToAddress("0x00000000000000000000000000000000000009")
	packed, err := event.Inputs.NonIndexed().Pack(big.NewInt(500), big.NewInt(7), auditor)
	if err != nil {
		t.Fatal(err)
	}
	recordKey := common.HexToHash("0x01")
	metadataDigest := common.HexToHash("0x02")
	vault := common.HexToAddress("0x0000000000000000000000000000000000000A")
	topics := []common.Hash{event.ID, recordKey, metadataDigest, common.BytesToHash(vault.Bytes())}

	ev, err := s.UnpackMinted(packed, topics)
	if err != nil {
		t.Fatal(err)
	}
	if ev.RecordKey != recordKey || ev.MetadataDigest != metadataDigest || ev.Vault != vault {
		t.Errorf("indexed fields mismatch: %+v", ev)
	}
	if ev.Amount.Cmp(big.NewInt(500)) != 0 || ev.Nonce.Cmp(big.NewInt(7)) != 0 || ev.Auditor != auditor {
		t.Errorf("non-indexed fields mismatch: %+v", ev)
	}
}

func TestVaultInventoryUnpack(t *testing.T) {
	v := NewVault()
	method := v.ABI.Methods["inventory"]
	packed, err := method.Outputs.Pack(big.NewInt(123456))
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.UnpackInventory(packed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(big.NewInt(123456)) != 0 {
		t.Errorf("got %s, want 123456", got)
	}
}

func TestRedemptionEscrowGetRedemptionRoundTrip(t *testing.T) {
	r := NewRedemptionEscrow()
	method := r.ABI.Methods["getRedemption"]
	want := RedemptionRequestTuple{
		Beneficiary: common.HexToAddress("0x0000000000000000000000000000000000000E"),
		RWAAmount:   big.NewInt(1000),
		QuoteAmount: big.NewInt(950),
		CreatedAt:   1700000000,
		Status:      1,
	}
	packed, err := method.Outputs.Pack(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.UnpackGetRedemption(packed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Beneficiary != want.Beneficiary || got.CreatedAt != want.CreatedAt || got.Status != want.Status ||
		got.RWAAmount.Cmp(want.RWAAmount) != 0 || got.QuoteAmount.Cmp(want.QuoteAmount) != 0 {
		t.Errorf("got %+v want %+v", got, want)
	}
}

// TestRedemptionEscrowRequestedEventDecode is a regression test for a real
// bug this exact call hit in server/e2e/run_e2e.sh: go-ethereum's untagged
// struct-field abi matching maps "rwaAmount" to the Go field name
// ToCamelCase("rwaAmount") == "RwaAmount" (only the first character is
// upcased), which never matches this codebase's "RWA" all-caps acronym
// spelling — UnpackIntoInterface failed with "abi: field rwaAmount can't be
// found in the given value" until the struct gained an explicit `abi:` tag.
func TestRedemptionEscrowRequestedEventDecode(t *testing.T) {
	r := NewRedemptionEscrow()
	event := r.ABI.Events["RedemptionRequested"]
	packed, err := event.Inputs.NonIndexed().Pack(big.NewInt(1000), big.NewInt(950), uint64(1700000000))
	if err != nil {
		t.Fatal(err)
	}
	id := big.NewInt(9)
	beneficiary := common.HexToAddress("0x0000000000000000000000000000000000000E")
	topics := []common.Hash{event.ID, common.BigToHash(id), common.BytesToHash(beneficiary.Bytes())}

	ev, err := r.UnpackRedemptionRequested(packed, topics)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID.Cmp(id) != 0 || ev.Beneficiary != beneficiary {
		t.Errorf("indexed fields mismatch: %+v", ev)
	}
	if ev.RWAAmount.Cmp(big.NewInt(1000)) != 0 || ev.QuoteAmount.Cmp(big.NewInt(950)) != 0 || ev.CreatedAt != 1700000000 {
		t.Errorf("non-indexed fields mismatch: %+v", ev)
	}
}

func TestRedemptionEscrowCompletedEventDecode(t *testing.T) {
	r := NewRedemptionEscrow()
	event := r.ABI.Events["RedemptionCompleted"]
	packed, err := event.Inputs.NonIndexed().Pack(big.NewInt(1000), big.NewInt(950))
	if err != nil {
		t.Fatal(err)
	}
	id := big.NewInt(9)
	beneficiary := common.HexToAddress("0x0000000000000000000000000000000000000E")
	topics := []common.Hash{event.ID, common.BigToHash(id), common.BytesToHash(beneficiary.Bytes())}

	ev, err := r.UnpackRedemptionCompleted(packed, topics)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID.Cmp(id) != 0 || ev.Beneficiary != beneficiary {
		t.Errorf("indexed fields mismatch: %+v", ev)
	}
	if ev.RWAAmount.Cmp(big.NewInt(1000)) != 0 || ev.QuoteAmount.Cmp(big.NewInt(950)) != 0 {
		t.Errorf("non-indexed fields mismatch: %+v", ev)
	}
}

func TestFactoryDeployPackUnpack(t *testing.T) {
	f := NewFactory()
	cfg := ProjectConfig{
		Name: "Gold Token", Symbol: "GLD", Decimals: 18,
		ProfileDigest: [32]byte{1}, ProjectID: [32]byte{2},
		QuoteToken:                   common.HexToAddress("0x0000000000000000000000000000000000000F"),
		PurchasePricePerWholeToken:   big.NewInt(2000000),
		RedemptionPricePerWholeToken: big.NewInt(1950000),
		RedemptionTimeout:            1209600,
		Admin:                        common.HexToAddress("0x0000000000000000000000000000000000AA01"),
		Auditor:                      common.HexToAddress("0x0000000000000000000000000000000000AA02"),
		ComplianceOperator:           common.HexToAddress("0x0000000000000000000000000000000000AA03"),
		Pricer:                       common.HexToAddress("0x0000000000000000000000000000000000AA04"),
		Treasurer:                    common.HexToAddress("0x0000000000000000000000000000000000AA05"),
		RedemptionManager:            common.HexToAddress("0x0000000000000000000000000000000000AA06"),
		Treasury:                     common.HexToAddress("0x0000000000000000000000000000000000AA07"),
		AdminTransferDelay:           big.NewInt(0),
	}
	data, err := f.PackDeploy(cfg)
	if err != nil {
		t.Fatalf("PackDeploy: %v", err)
	}
	args, err := f.ABI.Methods["deploy"].Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatal(err)
	}
	got := abi.ConvertType(args[0], new(ProjectConfig)).(*ProjectConfig)
	if got.Name != cfg.Name || got.Symbol != cfg.Symbol || got.Decimals != cfg.Decimals {
		t.Errorf("basic fields mismatch: %+v", got)
	}
	if got.QuoteToken != cfg.QuoteToken || got.Admin != cfg.Admin || got.Treasury != cfg.Treasury {
		t.Errorf("address fields mismatch: %+v", got)
	}
	if got.PurchasePricePerWholeToken.Cmp(cfg.PurchasePricePerWholeToken) != 0 {
		t.Errorf("price mismatch")
	}
}

func TestFactoryProjectDeployedEventDecode(t *testing.T) {
	f := NewFactory()
	event := f.ABI.Events["ProjectDeployed"]
	token := common.HexToAddress("0x0000000000000000000000000000000000B001")
	compliance := common.HexToAddress("0x0000000000000000000000000000000000B002")
	supplyController := common.HexToAddress("0x0000000000000000000000000000000000B003")
	vault := common.HexToAddress("0x0000000000000000000000000000000000B004")
	redemptionEscrow := common.HexToAddress("0x0000000000000000000000000000000000B005")
	strategy := common.HexToAddress("0x0000000000000000000000000000000000B006")

	packed, err := event.Inputs.NonIndexed().Pack(token, compliance, supplyController, vault, redemptionEscrow, strategy, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	projectID := common.HexToHash("0x03")
	profileDigest := common.HexToHash("0x04")
	topics := []common.Hash{event.ID, projectID, profileDigest}

	ev, err := f.UnpackProjectDeployed(packed, topics)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ProjectID != projectID || ev.ProfileDigest != profileDigest {
		t.Errorf("indexed mismatch: %+v", ev)
	}
	if ev.Token != token || ev.Vault != vault || ev.Version != "1.0.0" {
		t.Errorf("non-indexed mismatch: %+v", ev)
	}
}

func TestERC20BalanceOfRoundTrip(t *testing.T) {
	e := NewERC20()
	acct := common.HexToAddress("0x0000000000000000000000000000000000C001")
	data, err := e.PackBalanceOf(acct)
	if err != nil {
		t.Fatal(err)
	}
	args, err := e.ABI.Methods["balanceOf"].Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatal(err)
	}
	if args[0].(common.Address) != acct {
		t.Errorf("account mismatch")
	}

	packedReturn, err := e.ABI.Methods["balanceOf"].Outputs.Pack(big.NewInt(42))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.UnpackBalanceOf(packedReturn)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(big.NewInt(42)) != 0 {
		t.Errorf("got %s, want 42", got)
	}
}
