package income

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/rwa-platform/server/internal/dal/models"
)

var (
	vaultA  = common.HexToAddress("0x00000000000000000000000000000000000000f1")
	escrowA = common.HexToAddress("0x00000000000000000000000000000000000000f2")
	alice   = common.HexToAddress("0x00000000000000000000000000000000000000a1")
	bob     = common.HexToAddress("0x00000000000000000000000000000000000000b2")
	carol   = common.HexToAddress("0x00000000000000000000000000000000000000c3")
	zeroA   = common.Address{}
)

func transferEv(block uint64, idx uint, from, to common.Address, value string) *models.ChainEvent {
	return &models.ChainEvent{
		Name: "Transfer", BlockNumber: block, LogIndex: idx, TxHash: fmt.Sprintf("0xtx%d_%d", block, idx),
		Data: map[string]any{"from": from.Hex(), "to": to.Hex(), "value": value},
	}
}

func redemptionEv(name string, block uint64, idx uint, data map[string]any) *models.ChainEvent {
	return &models.ChainEvent{Name: name, BlockNumber: block, LogIndex: idx, TxHash: fmt.Sprintf("0xr%d_%d", block, idx), Data: data}
}

func requested(block uint64, id string, beneficiary common.Address, rwa string) *models.ChainEvent {
	return redemptionEv("RedemptionRequested", block, 5, map[string]any{
		"id": id, "beneficiary": beneficiary.Hex(), "rwaAmount": rwa, "quoteAmount": "1", "createdAt": int64(1000),
	})
}

func supplyEv(name string, block uint64, amount string) *models.ChainEvent {
	return &models.ChainEvent{Name: name, BlockNumber: block, TxHash: fmt.Sprintf("0xs%d", block), Data: map[string]any{"amount": amount}}
}

func amountsByAccount(t *testing.T, e *Entitlements) map[common.Address][2]string {
	t.Helper()
	out := map[common.Address][2]string{}
	for _, en := range e.Entries {
		out[en.Account] = [2]string{en.Balance.String(), en.Amount.String()}
	}
	return out
}

// baseTransfers mints 1000 to the Vault, sells 300 to alice and 100 to bob, and has alice
// pass 50 on to carol at block 20
func baseTransfers() []*models.ChainEvent {
	return []*models.ChainEvent{
		transferEv(1, 0, zeroA, vaultA, "1000"),
		transferEv(5, 0, vaultA, alice, "300"),
		transferEv(6, 0, vaultA, bob, "100"),
		transferEv(20, 0, alice, carol, "50"),
	}
}

func TestComputeEntitlements(t *testing.T) {
	ten := func(s string) *big.Int { n, _ := new(big.Int).SetString(s, 10); return n }
	cases := []struct {
		name    string
		in      EntitlementInput
		want    map[common.Address][2]string // balance, amount
		eligble string
		dist    string
		wantErr error
	}{
		{
			name: "vault inventory is excluded and shares are pro rata",
			in:   EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(1000), Transfers: baseTransfers()},
			want: map[common.Address][2]string{alice: {"300", "750"}, bob: {"100", "250"}},
			// 400 eligible, so 1000 * 300 / 400 and 1000 * 100 / 400
			eligble: "400", dist: "1000",
		},
		{
			name: "record block is inclusive and later transfers are ignored",
			in:   EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 20, Total: big.NewInt(400), Transfers: baseTransfers()},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			name: "block just below a transfer leaves it out",
			in:   EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 19, Total: big.NewInt(400), Transfers: baseTransfers()},
			want: map[common.Address][2]string{alice: {"300", "300"}, bob: {"100", "100"}}, eligble: "400", dist: "400",
		},
		{
			name: "removed events are ignored",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(400), Transfers: append(baseTransfers(),
				&models.ChainEvent{Name: "Transfer", BlockNumber: 7, Removed: true, Data: map[string]any{"from": vaultA.Hex(), "to": carol.Hex(), "value": "100"}})},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			// transferFrom(a, b, 0) needs no balance or allowance, so anyone can emit one from a
			// never-funded address, the escrow before its first redemption included
			name: "zero-value transfers from never-funded addresses move nothing",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(400), Transfers: append(baseTransfers(),
				transferEv(21, 0, common.HexToAddress("0x00000000000000000000000000000000000000d4"), bob, "0"),
				transferEv(22, 0, escrowA, alice, "0"),
				transferEv(23, 0, carol, common.HexToAddress("0x00000000000000000000000000000000000000e5"), "0"))},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			name: "pending redemption credits the beneficiary for escrowed RWA",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(400), RedemptionTimeout: 100,
				Transfers:        append(baseTransfers(), transferEv(25, 0, alice, escrowA, "100")),
				RedemptionEvents: []*models.ChainEvent{requested(25, "1", alice, "100")}},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			name: "funded redemption still counts",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(400), RedemptionTimeout: 100,
				Transfers: append(baseTransfers(), transferEv(25, 0, alice, escrowA, "100")),
				RedemptionEvents: []*models.ChainEvent{requested(25, "1", alice, "100"),
					redemptionEv("RedemptionFunded", 26, 0, map[string]any{"id": "1", "funder": bob.Hex(), "quoteAmount": "1"})}},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			name: "completed redemption returns RWA to the vault and is excluded",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(300), RedemptionTimeout: 100,
				Transfers: append(baseTransfers(), transferEv(25, 0, alice, escrowA, "100"), transferEv(27, 0, escrowA, vaultA, "100")),
				RedemptionEvents: []*models.ChainEvent{requested(25, "1", alice, "100"),
					redemptionEv("RedemptionFunded", 26, 0, map[string]any{"id": "1", "funder": bob.Hex(), "quoteAmount": "1"}),
					redemptionEv("RedemptionCompleted", 27, 1, map[string]any{"id": "1", "beneficiary": alice.Hex(), "rwaAmount": "100", "quoteAmount": "1"})}},
			want: map[common.Address][2]string{alice: {"150", "150"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "300", dist: "300",
		},
		{
			name: "cancelled redemption is counted through the returned balance, not twice",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(400), RedemptionTimeout: 100,
				Transfers: append(baseTransfers(), transferEv(25, 0, alice, escrowA, "100"), transferEv(28, 0, escrowA, alice, "100")),
				RedemptionEvents: []*models.ChainEvent{requested(25, "1", alice, "100"),
					redemptionEv("RedemptionCancelled", 28, 1, map[string]any{"id": "1", "beneficiary": alice.Hex()})}},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			name: "redemption requested after the record block is not attributed",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 24, Total: big.NewInt(400), RedemptionTimeout: 100,
				Transfers:        append(baseTransfers(), transferEv(25, 0, alice, escrowA, "100")),
				RedemptionEvents: []*models.ChainEvent{requested(25, "1", alice, "100")}},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}, carol: {"50", "50"}}, eligble: "400", dist: "400",
		},
		{
			name: "donation to the escrow is not attributed to anyone",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 30, Total: big.NewInt(350),
				Transfers: append(baseTransfers(), transferEv(25, 0, carol, escrowA, "50"))},
			want: map[common.Address][2]string{alice: {"250", "250"}, bob: {"100", "100"}}, eligble: "350", dist: "350",
		},
		{
			name: "rounding dust stays out of the entries",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(10), Transfers: []*models.ChainEvent{
				transferEv(1, 0, zeroA, alice, "1"), transferEv(1, 1, zeroA, bob, "1"), transferEv(1, 2, zeroA, carol, "1")}},
			want: map[common.Address][2]string{alice: {"1", "3"}, bob: {"1", "3"}, carol: {"1", "3"}}, eligble: "3", dist: "9",
		},
		{
			name: "zero amounts are dropped but their balance stays in the denominator",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(10), Transfers: []*models.ChainEvent{
				transferEv(1, 0, zeroA, alice, "1000"), transferEv(1, 1, zeroA, bob, "1")}},
			want: map[common.Address][2]string{alice: {"1000", "9"}}, eligble: "1001", dist: "9",
		},
		{
			name: "every amount rounds to zero",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(2), Transfers: []*models.ChainEvent{
				transferEv(1, 0, zeroA, alice, "1"), transferEv(1, 1, zeroA, bob, "1"), transferEv(1, 2, zeroA, carol, "1")}},
			wantErr: ErrNoEligibleHolders,
		},
		{
			name:    "only the vault holds RWA",
			in:      EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(10), Transfers: []*models.ChainEvent{transferEv(1, 0, zeroA, vaultA, "5")}},
			wantErr: ErrNoEligibleHolders,
		},
		{
			name:    "no transfers at all",
			in:      EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(10)},
			wantErr: ErrNoEligibleHolders,
		},
		{
			name: "a sender with no replayed balance means the history is incomplete",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(10),
				Transfers: []*models.ChainEvent{transferEv(5, 0, alice, bob, "1")}},
			wantErr: ErrIncompleteHistory,
		},
		{
			name: "burned RWA leaves the supply",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(100), Transfers: []*models.ChainEvent{
				transferEv(1, 0, zeroA, alice, "10"), transferEv(2, 0, alice, zeroA, "4"), transferEv(3, 0, zeroA, bob, "6")}},
			want: map[common.Address][2]string{alice: {"6", "50"}, bob: {"6", "50"}}, eligble: "12", dist: "100",
		},
		{
			name: "huge balances keep full precision",
			in: EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: ten("1000000000000000000000000000000"), Transfers: []*models.ChainEvent{
				transferEv(1, 0, zeroA, alice, "300000000000000000000000000001"), transferEv(1, 1, zeroA, bob, "700000000000000000000000000001")}},
			want: map[common.Address][2]string{
				alice: {"300000000000000000000000000001", "300000000000000000000000000000"},
				bob:   {"700000000000000000000000000001", "699999999999999999999999999999"}},
			eligble: "1000000000000000000000000000002", dist: "999999999999999999999999999999",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeEntitlements(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !equalAmounts(amountsByAccount(t, got), tc.want) {
				t.Errorf("entries = %v, want %v", amountsByAccount(t, got), tc.want)
			}
			if got.EligibleSupply.String() != tc.eligble {
				t.Errorf("eligibleSupply = %s, want %s", got.EligibleSupply, tc.eligble)
			}
			if got.Distributed.String() != tc.dist {
				t.Errorf("distributed = %s, want %s", got.Distributed, tc.dist)
			}
			if !sort.SliceIsSorted(got.Entries, func(i, j int) bool {
				return strings.ToLower(got.Entries[i].Account.Hex()) < strings.ToLower(got.Entries[j].Account.Hex())
			}) {
				t.Error("entries are not sorted by lowercase account hex")
			}
		})
	}
}

func equalAmounts(a, b map[common.Address][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestComputeEntitlementsSupplyCrossCheck(t *testing.T) {
	in := EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(100), Transfers: []*models.ChainEvent{
		transferEv(1, 0, zeroA, vaultA, "1000"), transferEv(2, 0, vaultA, alice, "10"), transferEv(3, 0, alice, zeroA, "4")},
		CheckSupply: true}

	// replayed supply is 996: minted 1000, burned 4
	in.Minted, in.Burned = []*models.ChainEvent{supplyEv("Minted", 1, "1000")}, []*models.ChainEvent{supplyEv("Burned", 3, "4")}
	if _, err := ComputeEntitlements(in); err != nil {
		t.Fatalf("consistent supply rejected: %v", err)
	}

	in.Minted = []*models.ChainEvent{supplyEv("Minted", 1, "1001")}
	if _, err := ComputeEntitlements(in); !errors.Is(err, ErrIncompleteHistory) {
		t.Errorf("a Transfer history missing 1 unit of mint was accepted: %v", err)
	}

	// a Minted after the record block does not count
	in.Minted = []*models.ChainEvent{supplyEv("Minted", 1, "1000"), supplyEv("Minted", 50, "7")}
	if _, err := ComputeEntitlements(in); err != nil {
		t.Errorf("later mint was counted: %v", err)
	}

	// with the check off the supply events are not consulted
	in.CheckSupply = false
	in.Minted = nil
	if _, err := ComputeEntitlements(in); err != nil {
		t.Errorf("check disabled but still failed: %v", err)
	}
}

func snapshotParams(t *testing.T, ent *Entitlements) SnapshotParams {
	t.Helper()
	return SnapshotParams{
		ChainID: 31337, Token: common.HexToAddress("0x000000000000000000000000000000000000b001"),
		IncomeDistributor: common.HexToAddress("0x000000000000000000000000000000000000b007"),
		Quote:             common.HexToAddress("0x000000000000000000000000000000000000aa01"),
		RecordBlock:       10, Total: big.NewInt(1000), Entitlements: ent,
	}
}

func TestBuildSnapshotMatchesSchemaAndTree(t *testing.T) {
	ent, err := ComputeEntitlements(EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(1000), Transfers: baseTransfers()})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := BuildSnapshot(snapshotParams(t, ent))
	if err != nil {
		t.Fatal(err)
	}

	schemaPath, err := filepath.Abs(filepath.Join("..", "..", "..", "shared", "schemas", "income-snapshot.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.NewCompiler().Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	var generic any
	if err := json.Unmarshal(snap.Canonical, &generic); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(generic); err != nil {
		t.Fatalf("snapshot violates shared/schemas/income-snapshot.schema.json: %v\n%s", err, snap.Canonical)
	}

	// RFC 8785: keys sorted, no insignificant whitespace
	if bytes.ContainsAny(snap.Canonical, " \n\t") {
		t.Errorf("canonical bytes hold whitespace: %s", snap.Canonical)
	}
	if !strings.HasPrefix(string(snap.Canonical), `{"chainId":"31337","distributed":`) {
		t.Errorf("keys are not in JCS order: %.80s", snap.Canonical)
	}
	if snap.Sha256 != sha256.Sum256(snap.Canonical) {
		t.Error("Sha256 is not the SHA-256 of the canonical bytes")
	}

	parsed, err := parseSnapshot(snap.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.tree.Root() != snap.MerkleRoot {
		t.Error("tree rebuilt from the document has a different root")
	}
	if snap.Doc.Entries[0].Account != alice.Hex() || snap.Doc.Entries[1].Account != bob.Hex() {
		t.Errorf("entries not EIP-55 and sorted: %+v", snap.Doc.Entries)
	}
}

func TestBuildSnapshotIsDeterministic(t *testing.T) {
	ent, _ := ComputeEntitlements(EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(1000), Transfers: baseTransfers()})
	a, err := BuildSnapshot(snapshotParams(t, ent))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := BuildSnapshot(snapshotParams(t, ent))
	if !bytes.Equal(a.Canonical, b.Canonical) || a.CID != b.CID {
		t.Error("the same entitlements produced different bytes")
	}
}

func TestParseSnapshotRejectsTamperedRoot(t *testing.T) {
	ent, _ := ComputeEntitlements(EntitlementInput{Vault: vaultA, Escrow: escrowA, RecordBlock: 10, Total: big.NewInt(1000), Transfers: baseTransfers()})
	snap, _ := BuildSnapshot(snapshotParams(t, ent))
	tampered := bytes.Replace(snap.Canonical, []byte(`"amount":"750"`), []byte(`"amount":"751"`), 1)
	if bytes.Equal(tampered, snap.Canonical) {
		t.Fatal("fixture did not change")
	}
	if _, err := parseSnapshot(tampered); err == nil {
		t.Error("a snapshot whose entries no longer hash to its root was accepted")
	}
}
