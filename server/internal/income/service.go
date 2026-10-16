package income

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
)

// MinClaimWindow adds an hour of mining slack to IncomeDistributor's 7-day minimum;
// the transaction must still meet that minimum at inclusion
const MinClaimWindow = 7*24*time.Hour + time.Hour

// Request errors, mapped to HTTP statuses by the API layer
var (
	// ErrInvalidRequest wraps a malformed or non-positive request field
	ErrInvalidRequest = errors.New("income: invalid request")
	// ErrClaimWindowTooShort means claimDeadline is under MinClaimWindow from now
	ErrClaimWindowTooShort = errors.New("income: claimDeadline must be at least 7 days and 1 hour from now")
	// ErrIncomeUnsupported means the project is not an Active rwa-v3 deployment with an income distributor
	ErrIncomeUnsupported = errors.New("income: the project is not an active rwa-v3 deployment with an income distributor")
	// ErrRecordBlockNotFinal means recordBlock is above the last indexed block minus the finality depth
	ErrRecordBlockNotFinal = errors.New("income: recordBlock is not final in the indexed history")
	// ErrAlreadyDistributed means a distribution of the identical snapshot already exists on-chain
	ErrAlreadyDistributed = errors.New("income: a distribution of this snapshot already exists on-chain")
	// ErrIPFSUnavailable means the snapshot could not be pinned
	ErrIPFSUnavailable = errors.New("income: pinning the snapshot to IPFS failed")
	// ErrSnapshotTooLarge means the canonical snapshot exceeds MaxSnapshotBytes
	ErrSnapshotTooLarge = errors.New("income: the snapshot is too large to pin as one IPFS block")
)

// MaxSnapshotBytes caps snapshots at one raw IPFS block so AddRaw's CID matches CIDv1Raw.
// Chunked UnixFS support would need a locally computed DAG CID.
const MaxSnapshotBytes = auditpkg.MaxDocumentBytes

// Pinner publishes snapshot bytes; *ipfs.KuboClient and *ipfs.ReplicationManager satisfy it
type Pinner interface {
	AddRaw(ctx context.Context, data []byte) (string, error)
}

// Distribution statuses reported by the API
const (
	StatusPrepared  = "Prepared"
	StatusActive    = "Active"
	StatusExpired   = "Expired"
	StatusReclaimed = "Reclaimed"
)

// Claim statuses reported by the API
const (
	ClaimClaimable = "Claimable"
	ClaimClaimed   = "Claimed"
	ClaimExpired   = "Expired"
)

// Service prepares snapshots and serves distribution/claim state, resolving addresses
// and redemption timeout from the stored Project across adoption and strategy changes
type Service struct {
	chainID     int64
	projects    repository.ProjectRepository
	chainEvents repository.ChainEventRepository
	checkpoints repository.IndexerCheckpointRepository
	preps       repository.IncomePreparationRepository
	dists       repository.IncomeDistributionRepository
	claims      repository.IncomeClaimRepository
	pinner      Pinner
	finality    uint64
	now         func() time.Time

	// parsed caches immutable snapshots by preparation ID without eviction;
	// use an LRU if preparations reach the thousands
	parsed sync.Map
}

// New constructs a Service; pinner may be nil, which makes Prepare fail with ErrIPFSUnavailable
func New(chainID int64, repos *repository.Repositories, pinner Pinner, finalityConfirmations uint64) *Service {
	return &Service{
		chainID: chainID, projects: repos.Projects, chainEvents: repos.ChainEvents, checkpoints: repos.IndexerCheckpoints,
		preps: repos.IncomePreparations, dists: repos.IncomeDistributions, claims: repos.IncomeClaims,
		pinner: pinner, finality: finalityConfirmations, now: func() time.Time { return time.Now().UTC() },
	}
}

// PrepareRequest is the input of Prepare
type PrepareRequest struct {
	RecordBlock   uint64
	Total         string
	ClaimDeadline int64
}

// activeIncomeProject loads the project and requires an Active rwa-v3 deployment with an
// income distributor
func (s *Service) activeIncomeProject(ctx context.Context) (*models.Project, error) {
	p, err := s.projects.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrIncomeUnsupported
	}
	if err != nil {
		return nil, err
	}
	if p.Status != models.ProjectStatusActive || p.Version != bindings.FactoryVersionV3 || p.Addresses.IncomeDistributor == "" {
		return nil, ErrIncomeUnsupported
	}
	return p, nil
}

// Prepare builds the snapshot for req from confirmed indexed events, pins its canonical
// bytes and stores a Prepared distribution. recordBlock must be at or below the last
// indexed block minus the finality depth.
func (s *Service) Prepare(ctx context.Context, req PrepareRequest) (*models.IncomePreparation, error) {
	total, ok := new(big.Int).SetString(req.Total, 10)
	if !ok || total.Sign() <= 0 || strings.ContainsAny(req.Total, "+-_ ") || (len(req.Total) > 1 && req.Total[0] == '0') {
		return nil, fmt.Errorf("%w: total must be a positive base-10 integer", ErrInvalidRequest)
	}
	if total.BitLen() > 256 {
		return nil, fmt.Errorf("%w: total does not fit in uint256", ErrInvalidRequest)
	}
	now := s.now()
	if req.ClaimDeadline < now.Add(MinClaimWindow).Unix() {
		return nil, ErrClaimWindowTooShort
	}

	p, err := s.activeIncomeProject(ctx)
	if err != nil {
		return nil, err
	}

	cp, err := s.checkpoints.Get(ctx, s.chainID, indexer.CheckpointAddress)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrRecordBlockNotFinal
	}
	if err != nil {
		return nil, err
	}
	if cp.ReconciliationRequired || cp.LastBlock < s.finality || req.RecordBlock > cp.LastBlock-s.finality {
		return nil, ErrRecordBlockNotFinal
	}

	a := p.Addresses
	token, vault, escrow := common.HexToAddress(a.Token), common.HexToAddress(a.Vault), common.HexToAddress(a.RedemptionEscrow)
	in := EntitlementInput{
		Vault: vault, Escrow: escrow, RecordBlock: req.RecordBlock, Total: total,
		RedemptionTimeout: p.RedemptionTimeout, CheckSupply: a.SupplyController != "",
	}
	if in.Transfers, err = s.chainEvents.ListByName(ctx, s.chainID, token.Hex(), "Transfer"); err != nil {
		return nil, fmt.Errorf("income: list Transfer events: %w", err)
	}
	for _, name := range []string{"RedemptionRequested", "RedemptionFunded", "RedemptionCompleted", "RedemptionRejected", "RedemptionCancelled"} {
		evs, err := s.chainEvents.ListByName(ctx, s.chainID, escrow.Hex(), name)
		if err != nil {
			return nil, fmt.Errorf("income: list %s events: %w", name, err)
		}
		in.RedemptionEvents = append(in.RedemptionEvents, evs...)
	}
	if in.CheckSupply {
		controller := common.HexToAddress(a.SupplyController).Hex()
		if in.Minted, err = s.chainEvents.ListByName(ctx, s.chainID, controller, "Minted"); err != nil {
			return nil, fmt.Errorf("income: list Minted events: %w", err)
		}
		if in.Burned, err = s.chainEvents.ListByName(ctx, s.chainID, controller, "Burned"); err != nil {
			return nil, fmt.Errorf("income: list Burned events: %w", err)
		}
	}

	ent, err := ComputeEntitlements(in)
	if err != nil {
		return nil, err
	}
	snap, err := BuildSnapshot(SnapshotParams{
		ChainID: s.chainID, Token: token, IncomeDistributor: common.HexToAddress(a.IncomeDistributor),
		Quote: common.HexToAddress(a.QuoteToken), RecordBlock: req.RecordBlock, Total: total, Entitlements: ent,
	})
	if err != nil {
		return nil, err
	}

	if len(snap.Canonical) > MaxSnapshotBytes {
		return nil, fmt.Errorf("%w: %d holders make %d bytes, over the %d-byte limit",
			ErrSnapshotTooLarge, len(ent.Entries), len(snap.Canonical), MaxSnapshotBytes)
	}
	if s.pinner == nil {
		return nil, fmt.Errorf("%w: no IPFS node is configured", ErrIPFSUnavailable)
	}
	pinned, err := s.pinner.AddRaw(ctx, snap.Canonical)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIPFSUnavailable, err)
	}
	if pinned != snap.CID {
		return nil, fmt.Errorf("%w: the node returned CID %s for content hashing to %s", ErrIPFSUnavailable, pinned, snap.CID)
	}

	// Snapshot bytes omit the deadline: reuse one preparation and update its deadline,
	// refusing snapshots with an indexed on-chain creation
	root, sha := "0x"+hex.EncodeToString(snap.MerkleRoot[:]), "0x"+hex.EncodeToString(snap.Sha256[:])
	dists, err := s.dists.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range dists {
		if snapshotKey(d.MerkleRoot, d.SnapshotSha256) == snapshotKey(root, sha) {
			return nil, ErrAlreadyDistributed
		}
	}
	if e, err := s.redatePreparation(ctx, root, sha, req.ClaimDeadline); err != nil || e != nil {
		return e, err
	}

	prep := &models.IncomePreparation{
		ID: uuid.NewString(), MerkleRoot: root, Total: total.String(),
		RecordBlock: req.RecordBlock, ClaimDeadline: req.ClaimDeadline,
		SnapshotURI: "ipfs://" + snap.CID, CID: snap.CID, SnapshotSha256: sha,
		EligibleSupply: ent.EligibleSupply.String(), Distributed: ent.Distributed.String(), HolderCount: len(ent.Entries),
		SnapshotRaw: snap.Canonical, CreatedAt: now,
	}
	err = s.preps.Create(ctx, prep)
	if errors.Is(err, repository.ErrAlreadyExists) {
		// a concurrent prepare stored the same snapshot first; the unique index on
		// (merkleRoot, snapshotSha256) keeps it to one preparation
		if e, err := s.redatePreparation(ctx, root, sha, req.ClaimDeadline); err != nil || e != nil {
			return e, err
		}
	}
	if err != nil {
		return nil, fmt.Errorf("income: store preparation: %w", err)
	}
	return prep, nil
}

// redatePreparation moves the deadline of the stored preparation of snapshot (root, sha)
// and returns it, or returns nil when there is none
func (s *Service) redatePreparation(ctx context.Context, root, sha string, deadline int64) (*models.IncomePreparation, error) {
	existing, err := s.preps.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range existing {
		if snapshotKey(e.MerkleRoot, e.SnapshotSha256) == snapshotKey(root, sha) {
			if err := s.preps.SetClaimDeadline(ctx, e.ID, deadline); err != nil {
				return nil, fmt.Errorf("income: update preparation deadline: %w", err)
			}
			e.ClaimDeadline = deadline
			return e, nil
		}
	}
	return nil, nil
}

// DistributionView is a prepared snapshot with its on-chain distribution, when created
type DistributionView struct {
	Prep *models.IncomePreparation
	// Dist is nil while the snapshot is Prepared
	Dist   *models.IncomeDistribution
	Status string
}

// statusOf derives the distribution status; claims are accepted through the deadline second
func statusOf(d *models.IncomeDistribution, now time.Time) string {
	switch {
	case d == nil:
		return StatusPrepared
	case d.Reclaimed:
		return StatusReclaimed
	case now.Unix() > d.ClaimDeadline:
		return StatusExpired
	default:
		return StatusActive
	}
}

func snapshotKey(root, sha string) string { return strings.ToLower(root) + "|" + strings.ToLower(sha) }

// List matches preparations oldest first to unused creation events by merkleRoot and
// snapshotSha256, then returns newest first. Further creations share the last preparation
// and remain visible as separate payouts. Without Active income, only preparations appear.
func (s *Service) List(ctx context.Context) ([]DistributionView, error) {
	preps, err := s.preps.List(ctx)
	if err != nil {
		return nil, err
	}
	dists, err := s.liveDistributions(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string][]*models.IncomeDistribution{} // creation order
	for _, d := range dists {
		k := snapshotKey(d.MerkleRoot, d.SnapshotSha256)
		byKey[k] = append(byKey[k], d)
	}
	lastPrep := map[string]int{}
	for i, p := range preps {
		lastPrep[snapshotKey(p.MerkleRoot, p.SnapshotSha256)] = i
	}

	now := s.now()
	out := make([]DistributionView, 0, len(preps))
	for i, p := range preps {
		k := snapshotKey(p.MerkleRoot, p.SnapshotSha256)
		var d *models.IncomeDistribution
		if q := byKey[k]; len(q) > 0 {
			d, byKey[k] = q[0], q[1:]
		}
		out = append(out, DistributionView{Prep: p, Dist: d, Status: statusOf(d, now)})
		if lastPrep[k] == i {
			for _, extra := range byKey[k] {
				out = append(out, DistributionView{Prep: p, Dist: extra, Status: statusOf(extra, now)})
			}
			byKey[k] = nil
		}
	}
	slices.Reverse(out) // newest first
	return out, nil
}

// liveDistributions returns the stored distributions, or none while the project is not an
// Active rwa-v3 deployment, so a demoted deployment's rows are not served before Reconcile
// prunes them
func (s *Service) liveDistributions(ctx context.Context) ([]*models.IncomeDistribution, error) {
	if _, err := s.activeIncomeProject(ctx); errors.Is(err, ErrIncomeUnsupported) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return s.dists.List(ctx)
}

// ClaimView is one wallet's entitlement in an on-chain distribution
type ClaimView struct {
	Dist    *models.IncomeDistribution
	Account common.Address
	Amount  *big.Int
	Proof   [][32]byte
	Status  string
	// ClaimTxHash is set when Status is ClaimClaimed
	ClaimTxHash string
}

// ClaimsFor lists account's entitlements newest first, skipping distributions without a
// server-prepared, readable snapshot or an entry for account. Bad snapshots leave other
// entitlements visible; request cancellation still propagates.
func (s *Service) ClaimsFor(ctx context.Context, account common.Address) ([]ClaimView, error) {
	dists, err := s.liveDistributions(ctx)
	if err != nil || len(dists) == 0 {
		return nil, err
	}
	preps, err := s.preps.List(ctx)
	if err != nil {
		return nil, err
	}
	bySnapshot := map[string]*models.IncomePreparation{}
	for _, p := range preps {
		if _, dup := bySnapshot[snapshotKey(p.MerkleRoot, p.SnapshotSha256)]; !dup {
			bySnapshot[snapshotKey(p.MerkleRoot, p.SnapshotSha256)] = p
		}
	}
	claimed := map[string]*models.IncomeClaim{}
	rows, err := s.claims.ListByAccount(ctx, account.Hex())
	if err != nil {
		return nil, err
	}
	for _, c := range rows {
		claimed[c.DistributionID] = c
	}

	now := s.now()
	var out []ClaimView
	for i := len(dists) - 1; i >= 0; i-- { // newest first
		d := dists[i]
		prep := bySnapshot[snapshotKey(d.MerkleRoot, d.SnapshotSha256)]
		if prep == nil {
			continue
		}
		snap, err := s.snapshotOf(ctx, prep)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			log.Printf("income: skipping distribution %s for claims: %v", d.ID, err)
			continue
		}
		entry, ok := snap.entries[strings.ToLower(account.Hex())]
		if !ok {
			continue
		}
		proof, err := snap.tree.Proof(entry.account, entry.amount)
		if err != nil {
			return nil, err
		}
		v := ClaimView{Dist: d, Account: entry.account, Amount: entry.amount, Proof: proof}
		switch c := claimed[d.ID]; {
		case c != nil:
			v.Status, v.ClaimTxHash = ClaimClaimed, c.TxHash
		case d.Reclaimed || now.Unix() > d.ClaimDeadline:
			v.Status = ClaimExpired
		default:
			v.Status = ClaimClaimable
		}
		out = append(out, v)
	}
	return out, nil
}

// snapshotOf rebuilds and caches a preparation's snapshot tree
func (s *Service) snapshotOf(ctx context.Context, p *models.IncomePreparation) (*parsedSnapshot, error) {
	if v, ok := s.parsed.Load(p.ID); ok {
		return v.(*parsedSnapshot), nil
	}
	// List leaves the raw snapshot out, so a cache miss loads the full document
	full, err := s.preps.Get(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("income: preparation %s: %w", p.ID, err)
	}
	snap, err := parseSnapshot(full.SnapshotRaw)
	if err != nil {
		return nil, fmt.Errorf("income: preparation %s: %w", p.ID, err)
	}
	if !strings.EqualFold(p.MerkleRoot, mustRoot(snap)) {
		return nil, fmt.Errorf("income: preparation %s: stored snapshot root differs from its record", p.ID)
	}
	s.parsed.Store(p.ID, snap)
	return snap, nil
}

func mustRoot(s *parsedSnapshot) string {
	r := s.tree.Root()
	return "0x" + hex.EncodeToString(r[:])
}

// BuildDistributions derives distribution and claim rows from IncomeDistributor events in
// (block, logIndex) order. Events for an id with no surviving DistributionCreated are ignored.
func BuildDistributions(events []*models.ChainEvent) (map[string]*models.IncomeDistribution, []*models.IncomeClaim) {
	sorted := make([]*models.ChainEvent, 0, len(events))
	for _, e := range events {
		if !e.Removed {
			sorted = append(sorted, e)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return earlier(sorted[i], sorted[j]) })

	dists := map[string]*models.IncomeDistribution{}
	claimedSum := map[string]*big.Int{}
	var claims []*models.IncomeClaim
	for _, e := range sorted {
		id := strField(e, "id")
		if id == "" {
			continue
		}
		switch e.Name {
		case "DistributionCreated":
			deadline, _ := new(big.Int).SetString(strField(e, "claimDeadline"), 10)
			block, _ := new(big.Int).SetString(strField(e, "recordBlock"), 10)
			if deadline == nil || block == nil {
				continue
			}
			dists[id] = &models.IncomeDistribution{
				ID: id, MerkleRoot: strField(e, "merkleRoot"), Total: strField(e, "total"),
				RecordBlock: block.Uint64(), ClaimDeadline: unixSeconds(deadline), SnapshotURI: strField(e, "snapshotURI"),
				SnapshotSha256: strField(e, "snapshotSha256"), Claimed: "0", CreateTxHash: e.TxHash,
				CreatedBlock: e.BlockNumber, CreatedLogIndex: e.LogIndex,
			}
			claimedSum[id] = new(big.Int)
		case "IncomeClaimed":
			d, ok := dists[id]
			amount, aok := new(big.Int).SetString(strField(e, "amount"), 10)
			account := strField(e, "account")
			if !ok || !aok || !common.IsHexAddress(account) {
				continue
			}
			claimedSum[id].Add(claimedSum[id], amount)
			d.Claimed = claimedSum[id].String()
			lower := strings.ToLower(common.HexToAddress(account).Hex())
			claims = append(claims, &models.IncomeClaim{
				ID: id + ":" + lower, DistributionID: id, Account: lower, Amount: amount.String(),
				TxHash: e.TxHash, BlockNumber: e.BlockNumber,
			})
		case "UnclaimedReclaimed":
			if d, ok := dists[id]; ok {
				d.Reclaimed = true
				d.ReclaimedAmount = strField(e, "amount")
			}
		case "ClaimDeadlineExtended":
			if d, ok := dists[id]; ok {
				if n, ok := new(big.Int).SetString(strField(e, "newDeadline"), 10); ok {
					d.ClaimDeadline = unixSeconds(n)
				}
			}
		}
	}
	return dists, claims
}

// unixSeconds converts an on-chain uint64 deadline, capping values above int64 (such as
// type(uint64).max for "never") at math.MaxInt64 instead of wrapping them negative
func unixSeconds(n *big.Int) int64 {
	if n.IsInt64() {
		return n.Int64()
	}
	return math.MaxInt64
}

// Reconcile rebuilds income rows from indexed events, upserting a fresh generation then
// pruning older rows to reflect rollbacks. Without an Active project and income distributor,
// it clears the read models; prepared snapshots remain.
func (s *Service) Reconcile(ctx context.Context) error {
	p, err := s.projects.Get(ctx)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	var dists map[string]*models.IncomeDistribution
	var claims []*models.IncomeClaim
	if err == nil && p.Status == models.ProjectStatusActive && p.Addresses.IncomeDistributor != "" {
		addr := common.HexToAddress(p.Addresses.IncomeDistributor).Hex()
		var all []*models.ChainEvent
		for _, name := range EventNames {
			evs, err := s.chainEvents.ListByName(ctx, s.chainID, addr, name)
			if err != nil {
				return fmt.Errorf("income: list %s events: %w", name, err)
			}
			all = append(all, evs...)
		}
		dists, claims = BuildDistributions(all)
	}
	gen := time.Now().UnixNano()
	for _, d := range dists {
		d.Generation = gen
		if err := s.dists.Upsert(ctx, d); err != nil {
			return fmt.Errorf("income: upsert distribution %s: %w", d.ID, err)
		}
	}
	for _, c := range claims {
		c.Generation = gen
		if err := s.claims.Upsert(ctx, c); err != nil {
			return fmt.Errorf("income: upsert claim %s: %w", c.ID, err)
		}
	}
	if err := s.dists.DeleteStaleGeneration(ctx, gen); err != nil {
		return fmt.Errorf("income: prune distributions: %w", err)
	}
	if err := s.claims.DeleteStaleGeneration(ctx, gen); err != nil {
		return fmt.Errorf("income: prune claims: %w", err)
	}
	return nil
}
