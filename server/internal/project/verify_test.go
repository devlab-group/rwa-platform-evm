package project

import (
	"context"
	"math/big"
	"testing"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/models"
)

// perContractClient routes eth_call responses by (contract, calldata) so a
// test can give the SAME role-enumeration calldata (getRoleMemberCount and
// getRoleMember are keyed only by role, not by contract) different answers
// per deployed contract — which the shared FakeClient, keyed solely by
// calldata, cannot. Unstubbed calls fall through to the embedded FakeClient,
// whose DefaultCallResponse newPerContractClient sets to 32 zero bytes so an
// un-stubbed getRoleMemberCount reports zero holders (and an un-stubbed
// hasRole reads false).
type perContractClient struct {
	*blockchain.FakeClient
	byContract map[common.Address]map[string][]byte
}

func newPerContractClient() *perContractClient {
	fc := blockchain.NewFakeClient()
	fc.DefaultCallResponse = make([]byte, 32) // uint256(0) / false
	return &perContractClient{FakeClient: fc, byContract: map[common.Address]map[string][]byte{}}
}

func (c *perContractClient) CallContract(ctx context.Context, msg ethereum.CallMsg, bn *big.Int) ([]byte, error) {
	if msg.To != nil {
		if m := c.byContract[*msg.To]; m != nil {
			if resp, ok := m[common.Bytes2Hex(msg.Data)]; ok {
				return resp, nil
			}
		}
	}
	return c.FakeClient.CallContract(ctx, msg, bn)
}

// stubRoleHolders makes contract enumerate exactly holders for role via
// AccessControlEnumerable's getRoleMemberCount/getRoleMember.
func stubRoleHolders(t *testing.T, c *perContractClient, contract common.Address, role [32]byte, holders ...common.Address) {
	t.Helper()
	ac := bindings.NewAccessControl()
	m := c.byContract[contract]
	if m == nil {
		m = map[string][]byte{}
		c.byContract[contract] = m
	}
	countData, err := ac.PackGetRoleMemberCount(role)
	if err != nil {
		t.Fatal(err)
	}
	countOut, err := ac.ABI.Methods["getRoleMemberCount"].Outputs.Pack(big.NewInt(int64(len(holders))))
	if err != nil {
		t.Fatal(err)
	}
	m[common.Bytes2Hex(countData)] = countOut
	for i, h := range holders {
		memberData, err := ac.PackGetRoleMember(role, big.NewInt(int64(i)))
		if err != nil {
			t.Fatal(err)
		}
		memberOut, err := ac.ABI.Methods["getRoleMember"].Outputs.Pack(h)
		if err != nil {
			t.Fatal(err)
		}
		m[common.Bytes2Hex(memberData)] = memberOut
	}
}

// stubExpectedRoleHolders wires the canonical, all-correct holder set for p
// (admin as DEFAULT_ADMIN on every contract + PAUSER on token; each
// operational role on its host contract) so a test can then add ONE
// unexpected/missing holder and confirm it (and only it) is detected.
func stubExpectedRoleHolders(t *testing.T, c *perContractClient, p *models.Project) {
	t.Helper()
	admin := common.HexToAddress(p.Admin)
	token := common.HexToAddress(p.Addresses.Token)
	compliance := common.HexToAddress(p.Addresses.Compliance)
	controller := common.HexToAddress(p.Addresses.SupplyController)
	vault := common.HexToAddress(p.Addresses.Vault)
	escrow := common.HexToAddress(p.Addresses.RedemptionEscrow)
	strategy := common.HexToAddress(p.Addresses.Strategy)
	for _, ct := range []common.Address{token, compliance, controller, vault, escrow, strategy} {
		stubRoleHolders(t, c, ct, bindings.DefaultAdminRole, admin)
	}
	stubRoleHolders(t, c, token, bindings.PauserRole, admin)
	if op := common.HexToAddress(p.ComplianceOperator); op != (common.Address{}) {
		stubRoleHolders(t, c, compliance, bindings.ComplianceRole, op)
	}
	if pr := common.HexToAddress(p.Pricer); pr != (common.Address{}) {
		// Strategy only — RWAFactory does not grant PRICER_ROLE on the Vault.
		stubRoleHolders(t, c, strategy, bindings.PricerRole, pr)
	}
	if tr := common.HexToAddress(p.Treasurer); tr != (common.Address{}) {
		stubRoleHolders(t, c, vault, bindings.TreasurerRole, tr)
		stubRoleHolders(t, c, escrow, bindings.TreasurerRole, tr)
	}
	if rm := common.HexToAddress(p.RedemptionManager); rm != (common.Address{}) {
		stubRoleHolders(t, c, escrow, bindings.RedemptionManagerRole, rm)
	}
}

func sampleRoleProject() *models.Project {
	return &models.Project{
		Addresses:          sampleAddresses(),
		Admin:              "0x0000000000000000000000000000000000AA02",
		ComplianceOperator: "0x0000000000000000000000000000000000AA04",
		Pricer:             "0x0000000000000000000000000000000000AA05",
		Treasurer:          "0x0000000000000000000000000000000000AA06",
		RedemptionManager:  "0x0000000000000000000000000000000000AA07",
	}
}

func sampleAddresses() models.Addresses {
	return models.Addresses{
		Token: "0x0000000000000000000000000000000000B001", Compliance: "0x0000000000000000000000000000000000B002",
		SupplyController: "0x0000000000000000000000000000000000B003", Vault: "0x0000000000000000000000000000000000B004",
		RedemptionEscrow: "0x0000000000000000000000000000000000B005", Strategy: "0x0000000000000000000000000000000000B006",
	}
}

func TestVerifyBytecodeAllPresent(t *testing.T) {
	client := blockchain.NewFakeClient()
	addrs := sampleAddresses()
	for _, a := range []string{addrs.Token, addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy} {
		client.Code[common.HexToAddress(a)] = []byte{0x60, 0x60}
	}
	ok, err := verifyBytecode(context.Background(), client, addrs)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected bytecodeVerified = true when every address has code")
	}
}

func TestVerifyBytecodeFalseWhenOneAddressEmpty(t *testing.T) {
	client := blockchain.NewFakeClient()
	addrs := sampleAddresses()
	// Seed all but Strategy — simulates a factory event pointing at an
	// address with nothing actually deployed there.
	for _, a := range []string{addrs.Token, addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow} {
		client.Code[common.HexToAddress(a)] = []byte{0x60, 0x60}
	}
	ok, err := verifyBytecode(context.Background(), client, addrs)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected bytecodeVerified = false when one address has no code")
	}
}

func TestVerifyRolesEnumeratesExpectedHolders(t *testing.T) {
	c := newPerContractClient()
	p := sampleRoleProject()
	stubExpectedRoleHolders(t, c, p)
	roles, missing, unexpected, err := verifyRoles(context.Background(), c, p)
	if err != nil {
		t.Fatalf("verifyRoles: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	if len(unexpected) != 0 {
		t.Errorf("unexpected = %v, want none", unexpected)
	}
	if got := roles["DEFAULT_ADMIN_ROLE"]; len(got) != 1 || got[0] != common.HexToAddress(p.Admin).Hex() {
		t.Errorf("DEFAULT_ADMIN_ROLE holders = %v", got)
	}
	if got := roles["COMPLIANCE_ROLE"]; len(got) != 1 {
		t.Errorf("COMPLIANCE_ROLE holders = %v, want exactly the compliance operator", got)
	}
}

// TestVerifyRolesDetectsUnexpectedHolder checks that a role held by an address
// NOT on the allowlist — an attacker granted admin, or a bootstrap holder
// never renounced — is reported as unexpected even though every required
// holder is also present. A candidate-only hasRole check would be structurally
// blind to this.
func TestVerifyRolesDetectsUnexpectedHolder(t *testing.T) {
	c := newPerContractClient()
	p := sampleRoleProject()
	stubExpectedRoleHolders(t, c, p)
	attacker := common.HexToAddress("0x000000000000000000000000000000000000DEAD")
	// An extra, unauthorized DEFAULT_ADMIN on the token alongside the real admin.
	stubRoleHolders(t, c, common.HexToAddress(p.Addresses.Token), bindings.DefaultAdminRole, common.HexToAddress(p.Admin), attacker)
	_, missing, unexpected, err := verifyRoles(context.Background(), c, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none (the required admin is still present)", missing)
	}
	if len(unexpected) != 1 {
		t.Fatalf("unexpected = %v, want exactly the extra admin", unexpected)
	}
}

// TestVerifyRolesDetectsMissingHolder: a required holder absent from a
// contract is reported.
func TestVerifyRolesDetectsMissingHolder(t *testing.T) {
	c := newPerContractClient()
	p := sampleRoleProject()
	stubExpectedRoleHolders(t, c, p)
	// Wipe DEFAULT_ADMIN on the vault (no holders enumerated there).
	stubRoleHolders(t, c, common.HexToAddress(p.Addresses.Vault), bindings.DefaultAdminRole)
	_, missing, unexpected, err := verifyRoles(context.Background(), c, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(unexpected) != 0 {
		t.Errorf("unexpected = %v, want none", unexpected)
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %v, want exactly the vault admin", missing)
	}
}

func TestVerifyRolesDedupesHolderAcrossMultipleContracts(t *testing.T) {
	c := newPerContractClient()
	p := sampleRoleProject()
	stubExpectedRoleHolders(t, c, p)
	roles, _, _, err := verifyRoles(context.Background(), c, p)
	if err != nil {
		t.Fatal(err)
	}
	// admin holds DEFAULT_ADMIN on all six contracts but must appear once.
	if got := roles["DEFAULT_ADMIN_ROLE"]; len(got) != 1 {
		t.Errorf("DEFAULT_ADMIN_ROLE holders = %v, want exactly 1 (deduped)", got)
	}
	// treasurer holds TREASURER on vault + escrow but must appear once.
	if got := roles["TREASURER_ROLE"]; len(got) != 1 {
		t.Errorf("TREASURER_ROLE holders = %v, want exactly 1 (deduped)", got)
	}
}
