package income

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gowebpki/jcs"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/redemption"
)

// Errors from building entitlements
var (
	// ErrNoEligibleHolders means no holder has a positive amount at the record block
	ErrNoEligibleHolders = errors.New("income: no eligible holders at the record block")
	// ErrIncompleteHistory means replayed balances or supply disagree with indexed events;
	// deployments indexed before Transfer logs were stored need reindexing
	ErrIncompleteHistory = errors.New("income: indexed Transfer history is incomplete; reindex the deployment")
)

// Entitlement is one holder's attributed RWA balance and income amount
type Entitlement struct {
	Account common.Address
	Balance *big.Int
	Amount  *big.Int
}

// Entitlements is the outcome of ComputeEntitlements
type Entitlements struct {
	// EligibleSupply is the sum of attributed balances, in RWA minimal units
	EligibleSupply *big.Int
	// Distributed is the sum of entry amounts
	Distributed *big.Int
	// Entries holds the holders with a positive amount, sorted by lowercase account hex
	Entries []Entitlement
}

// EntitlementInput carries the indexed events and addresses ComputeEntitlements replays
type EntitlementInput struct {
	Vault, Escrow common.Address
	RecordBlock   uint64
	Total         *big.Int
	// Transfers are the token's indexed "Transfer" events
	Transfers []*models.ChainEvent
	// RedemptionEvents are the escrow's indexed redemption events of every kind
	RedemptionEvents  []*models.ChainEvent
	RedemptionTimeout int64
	// Minted and Burned are the SupplyController's indexed events. When CheckSupply is
	// set, the supply replayed from Transfer must equal minted minus burned.
	Minted, Burned []*models.ChainEvent
	CheckSupply    bool
}

// ComputeEntitlements replays token Transfer events up to and including RecordBlock and
// splits Total pro rata over the resulting balances:
//   - the Vault's balance is unsold inventory and is excluded
//   - the escrow's balance is replaced by the RWA of each redemption request that is
//     Pending or Funded at RecordBlock, credited to its beneficiary, because the
//     beneficiary still owns that RWA until the claim completes
//   - frozen and Blocked holders count like any other holder
//   - amount = floor(Total * balance / eligibleSupply); zero amounts are dropped, and
//     their rounding dust stays unclaimed
func ComputeEntitlements(in EntitlementInput) (*Entitlements, error) {
	transfers := survivors(in.Transfers, in.RecordBlock)
	balances := map[common.Address]*big.Int{}
	supply := new(big.Int)
	zero := common.Address{}
	for _, e := range transfers {
		from, to, value, err := parseTransfer(e)
		if err != nil {
			return nil, err
		}
		// transferFrom(a, b, 0) needs no balance or allowance, so anyone can emit one from a
		// never-funded address; it moves nothing and must not read as missing history
		if value.Sign() == 0 {
			continue
		}
		if from == zero {
			supply.Add(supply, value)
		} else {
			b := balances[from]
			if b == nil || b.Cmp(value) < 0 {
				return nil, fmt.Errorf("%w (%s sends %s with a smaller replayed balance in tx %s)", ErrIncompleteHistory, from.Hex(), value, e.TxHash)
			}
			balances[from] = new(big.Int).Sub(b, value)
		}
		if to == zero {
			supply.Sub(supply, value)
		} else {
			b := balances[to]
			if b == nil {
				b = new(big.Int)
			}
			balances[to] = new(big.Int).Add(b, value)
		}
	}

	if in.CheckSupply {
		want := new(big.Int)
		for _, e := range survivors(in.Minted, in.RecordBlock) {
			n, ok := new(big.Int).SetString(strField(e, "amount"), 10)
			if !ok {
				return nil, fmt.Errorf("income: Minted event %s has no amount", e.TxHash)
			}
			want.Add(want, n)
		}
		for _, e := range survivors(in.Burned, in.RecordBlock) {
			n, ok := new(big.Int).SetString(strField(e, "amount"), 10)
			if !ok {
				return nil, fmt.Errorf("income: Burned event %s has no amount", e.TxHash)
			}
			want.Sub(want, n)
		}
		if want.Cmp(supply) != 0 {
			return nil, fmt.Errorf("%w (Transfer events replay a supply of %s, SupplyController events say %s)", ErrIncompleteHistory, supply, want)
		}
	}

	delete(balances, in.Vault)
	delete(balances, in.Escrow)
	delete(balances, zero)

	for _, r := range redemption.BuildStates(survivors(in.RedemptionEvents, in.RecordBlock), in.RedemptionTimeout) {
		if r.Status != models.RedemptionPending && r.Status != models.RedemptionFunded {
			continue
		}
		amount, ok := new(big.Int).SetString(r.RWAAmount, 10)
		beneficiary := common.HexToAddress(r.Beneficiary)
		if !ok || !common.IsHexAddress(r.Beneficiary) {
			return nil, fmt.Errorf("income: redemption %s has an unusable beneficiary or amount", r.ID)
		}
		if beneficiary == zero || beneficiary == in.Vault || beneficiary == in.Escrow {
			continue
		}
		b := balances[beneficiary]
		if b == nil {
			b = new(big.Int)
		}
		balances[beneficiary] = new(big.Int).Add(b, amount)
	}

	eligible := new(big.Int)
	for a, b := range balances {
		if b.Sign() == 0 {
			delete(balances, a)
			continue
		}
		eligible.Add(eligible, b)
	}
	if eligible.Sign() == 0 {
		return nil, ErrNoEligibleHolders
	}

	out := &Entitlements{EligibleSupply: eligible, Distributed: new(big.Int)}
	for a, b := range balances {
		amount := new(big.Int).Mul(in.Total, b)
		amount.Quo(amount, eligible)
		if amount.Sign() == 0 {
			continue
		}
		out.Entries = append(out.Entries, Entitlement{Account: a, Balance: b, Amount: amount})
		out.Distributed.Add(out.Distributed, amount)
	}
	if len(out.Entries) == 0 {
		return nil, ErrNoEligibleHolders
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return strings.ToLower(out.Entries[i].Account.Hex()) < strings.ToLower(out.Entries[j].Account.Hex())
	})
	return out, nil
}

// survivors returns the non-removed events at or below block, in (block, logIndex) order
func survivors(events []*models.ChainEvent, block uint64) []*models.ChainEvent {
	out := make([]*models.ChainEvent, 0, len(events))
	for _, e := range events {
		if !e.Removed && e.BlockNumber <= block {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return earlier(out[i], out[j]) })
	return out
}

func strField(e *models.ChainEvent, key string) string {
	s, _ := e.Data[key].(string)
	return s
}

func parseTransfer(e *models.ChainEvent) (from, to common.Address, value *big.Int, err error) {
	f, t, v := strField(e, "from"), strField(e, "to"), strField(e, "value")
	n, ok := new(big.Int).SetString(v, 10)
	if !common.IsHexAddress(f) || !common.IsHexAddress(t) || !ok || n.Sign() < 0 {
		return from, to, nil, fmt.Errorf("income: unusable Transfer event %s/%d (from=%q to=%q value=%q)", e.TxHash, e.LogIndex, f, t, v)
	}
	return common.HexToAddress(f), common.HexToAddress(t), n, nil
}

// SnapshotDoc is the published snapshot document, laid out as
// shared/schemas/income-snapshot.schema.json
type SnapshotDoc struct {
	FormatVersion     string          `json:"formatVersion"`
	ChainID           string          `json:"chainId"`
	Token             string          `json:"token"`
	IncomeDistributor string          `json:"incomeDistributor"`
	QuoteToken        string          `json:"quoteToken"`
	RecordBlock       string          `json:"recordBlock"`
	Total             string          `json:"total"`
	EligibleSupply    string          `json:"eligibleSupply"`
	Distributed       string          `json:"distributed"`
	MerkleRoot        string          `json:"merkleRoot"`
	LeafEncoding      []string        `json:"leafEncoding"`
	Entries           []SnapshotEntry `json:"entries"`
}

// SnapshotEntry is one holder's line of the snapshot document
type SnapshotEntry struct {
	Account string `json:"account"`
	Balance string `json:"balance"`
	Amount  string `json:"amount"`
}

// Snapshot is a built snapshot: its canonical bytes and the figures
// createDistribution takes
type Snapshot struct {
	Doc        SnapshotDoc
	Canonical  []byte
	Sha256     [32]byte
	CID        string
	MerkleRoot [32]byte
}

// SnapshotParams are the document fields that do not come from the entitlements
type SnapshotParams struct {
	ChainID                         int64
	Token, IncomeDistributor, Quote common.Address
	RecordBlock                     uint64
	Total                           *big.Int
	Entitlements                    *Entitlements
}

// BuildSnapshot builds the tree and RFC 8785 document using JCS directly to bypass the
// profile's 4096-item array limit; Prepare enforces MaxSnapshotBytes
func BuildSnapshot(p SnapshotParams) (*Snapshot, error) {
	leaves := make([]Leaf, len(p.Entitlements.Entries))
	entries := make([]SnapshotEntry, len(p.Entitlements.Entries))
	for i, e := range p.Entitlements.Entries {
		leaves[i] = Leaf{Account: e.Account, Amount: e.Amount}
		entries[i] = SnapshotEntry{Account: e.Account.Hex(), Balance: e.Balance.String(), Amount: e.Amount.String()}
	}
	tree, err := BuildTree(leaves)
	if err != nil {
		return nil, err
	}
	root := tree.Root()
	doc := SnapshotDoc{
		FormatVersion: "1", ChainID: fmt.Sprint(p.ChainID),
		Token: p.Token.Hex(), IncomeDistributor: p.IncomeDistributor.Hex(), QuoteToken: p.Quote.Hex(),
		RecordBlock: fmt.Sprint(p.RecordBlock), Total: p.Total.String(),
		EligibleSupply: p.Entitlements.EligibleSupply.String(), Distributed: p.Entitlements.Distributed.String(),
		MerkleRoot: "0x" + hex.EncodeToString(root[:]), LeafEncoding: []string{"address", "uint256"},
		Entries: entries,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("income: canonicalize snapshot: %w", err)
	}
	cid, err := auditpkg.CIDv1Raw(canonical)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Doc: doc, Canonical: canonical, Sha256: sha256.Sum256(canonical), CID: cid, MerkleRoot: root}, nil
}

// parsedSnapshot is a stored snapshot with its tree rebuilt, for serving proofs
type parsedSnapshot struct {
	tree    *Tree
	entries map[string]parsedEntry // lowercase account -> entry
}

type parsedEntry struct {
	account common.Address
	amount  *big.Int
}

// parseSnapshot reads canonical snapshot bytes back, rebuilds the tree and requires its
// root to equal the document's merkleRoot
func parseSnapshot(canonical []byte) (*parsedSnapshot, error) {
	var doc SnapshotDoc
	if err := json.Unmarshal(canonical, &doc); err != nil {
		return nil, fmt.Errorf("income: parse snapshot: %w", err)
	}
	leaves := make([]Leaf, len(doc.Entries))
	entries := make(map[string]parsedEntry, len(doc.Entries))
	for i, e := range doc.Entries {
		amount, ok := new(big.Int).SetString(e.Amount, 10)
		if !ok || !common.IsHexAddress(e.Account) {
			return nil, fmt.Errorf("income: snapshot entry %d is malformed", i)
		}
		a := common.HexToAddress(e.Account)
		leaves[i] = Leaf{Account: a, Amount: amount}
		entries[strings.ToLower(a.Hex())] = parsedEntry{account: a, amount: amount}
	}
	tree, err := BuildTree(leaves)
	if err != nil {
		return nil, err
	}
	root := tree.Root()
	if "0x"+hex.EncodeToString(root[:]) != strings.ToLower(doc.MerkleRoot) {
		return nil, errors.New("income: stored snapshot does not hash to its merkleRoot")
	}
	return &parsedSnapshot{tree: tree, entries: entries}, nil
}
