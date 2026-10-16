package income

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
)

const testChain int64 = 31337

var (
	tokenA      = common.HexToAddress("0x000000000000000000000000000000000000b001")
	controllerA = common.HexToAddress("0x000000000000000000000000000000000000b003")
	distribA    = common.HexToAddress("0x000000000000000000000000000000000000b007")
	quoteA      = common.HexToAddress("0x000000000000000000000000000000000000aa01")
	fixedNow    = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
)

type failingPinner struct{}

func (failingPinner) AddRaw(context.Context, []byte) (string, error) {
	return "", errors.New("dial tcp 10.0.0.7:5001: connection refused")
}

type wrongCIDPinner struct{}

func (wrongCIDPinner) AddRaw(context.Context, []byte) (string, error) { return "bafywrong", nil }

type env struct {
	svc   *Service
	repos *repository.Repositories
	ipfs  *ipfs.FakeClient
}

func ev(addr common.Address, e *models.ChainEvent) *models.ChainEvent {
	e.ChainID, e.Address = testChain, addr.Hex()
	return e
}

func newEnv(t *testing.T, mutate func(p *models.Project)) *env {
	t.Helper()
	return newEnvWith(t, memory.New(), mutate)
}

// newEnvWith seeds repos with an Active rwa-v3 project, a checkpoint at block 100 and the
// base Transfer history, and builds a Service on them with a fixed clock
func newEnvWith(t *testing.T, repos *repository.Repositories, mutate func(p *models.Project)) *env {
	t.Helper()
	p := &models.Project{
		ProjectID: "p", Version: "rwa-v3", ChainID: testChain, Status: models.ProjectStatusActive, RedemptionTimeout: 1000,
		Addresses: models.Addresses{
			Token: tokenA.Hex(), SupplyController: controllerA.Hex(), Vault: vaultA.Hex(), RedemptionEscrow: escrowA.Hex(),
			IncomeDistributor: distribA.Hex(), QuoteToken: quoteA.Hex(),
		},
	}
	if mutate != nil {
		mutate(p)
	}
	ctx := context.Background()
	if err := repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{ChainID: testChain, Address: indexer.CheckpointAddress, LastBlock: 100}); err != nil {
		t.Fatal(err)
	}
	for _, e := range baseTransfers() {
		if err := repos.ChainEvents.Create(ctx, ev(tokenA, e)); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []*models.ChainEvent{supplyEv("Minted", 1, "1000")} {
		if err := repos.ChainEvents.Create(ctx, ev(controllerA, e)); err != nil {
			t.Fatal(err)
		}
	}
	fake := ipfs.NewFakeClient()
	svc := New(testChain, repos, fake, 12)
	svc.now = func() time.Time { return fixedNow }
	return &env{svc: svc, repos: repos, ipfs: fake}
}

func validRequest() PrepareRequest {
	return PrepareRequest{RecordBlock: 30, Total: "1000", ClaimDeadline: fixedNow.Add(8 * 24 * time.Hour).Unix()}
}

func TestPrepareBuildsPinsAndStores(t *testing.T) {
	e := newEnv(t, nil)
	prep, err := e.svc.Prepare(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if prep.HolderCount != 3 || prep.Total != "1000" || prep.EligibleSupply != "400" || prep.RecordBlock != 30 {
		t.Errorf("figures: %+v", prep)
	}
	if !strings.HasPrefix(prep.SnapshotURI, "ipfs://") || prep.SnapshotURI != "ipfs://"+prep.CID {
		t.Errorf("snapshotURI = %q", prep.SnapshotURI)
	}
	pinned, err := e.ipfs.Get(context.Background(), prep.CID)
	if err != nil {
		t.Fatalf("snapshot was not pinned: %v", err)
	}
	if string(pinned) != string(prep.SnapshotRaw) {
		t.Error("pinned bytes differ from the stored canonical bytes")
	}
	stored, err := e.repos.IncomePreparations.Get(context.Background(), prep.ID)
	if err != nil || stored.MerkleRoot != prep.MerkleRoot {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	if len(prep.MerkleRoot) != 66 || len(prep.SnapshotSha256) != 66 {
		t.Errorf("root/sha are not 0x + 64 hex: %s %s", prep.MerkleRoot, prep.SnapshotSha256)
	}
}

func TestPrepareValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(r *PrepareRequest)
		wantErr error
	}{
		{"zero total", func(r *PrepareRequest) { r.Total = "0" }, ErrInvalidRequest},
		{"negative total", func(r *PrepareRequest) { r.Total = "-5" }, ErrInvalidRequest},
		{"non numeric total", func(r *PrepareRequest) { r.Total = "ten" }, ErrInvalidRequest},
		{"fractional total", func(r *PrepareRequest) { r.Total = "1.5" }, ErrInvalidRequest},
		{"empty total", func(r *PrepareRequest) { r.Total = "" }, ErrInvalidRequest},
		{"leading zero total", func(r *PrepareRequest) { r.Total = "01" }, ErrInvalidRequest},
		{"total above uint256", func(r *PrepareRequest) { r.Total = "1" + strings.Repeat("0", 78) }, ErrInvalidRequest},
		{"deadline in the past", func(r *PrepareRequest) { r.ClaimDeadline = fixedNow.Add(-time.Hour).Unix() }, ErrClaimWindowTooShort},
		{"deadline exactly 7 days", func(r *PrepareRequest) { r.ClaimDeadline = fixedNow.Add(7 * 24 * time.Hour).Unix() }, ErrClaimWindowTooShort},
		{"deadline one second short", func(r *PrepareRequest) { r.ClaimDeadline = fixedNow.Add(MinClaimWindow).Unix() - 1 }, ErrClaimWindowTooShort},
		{"deadline at the minimum", func(r *PrepareRequest) { r.ClaimDeadline = fixedNow.Add(MinClaimWindow).Unix() }, nil},
		{"record block at the finality limit", func(r *PrepareRequest) { r.RecordBlock = 88 }, nil}, // 100 - 12
		{"record block one past the finality limit", func(r *PrepareRequest) { r.RecordBlock = 89 }, ErrRecordBlockNotFinal},
		{"record block beyond the head", func(r *PrepareRequest) { r.RecordBlock = 5000 }, ErrRecordBlockNotFinal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			req := validRequest()
			tc.mutate(&req)
			_, err := e.svc.Prepare(context.Background(), req)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if list, _ := e.repos.IncomePreparations.List(context.Background()); len(list) != 0 {
					t.Errorf("a rejected request stored %d preparations", len(list))
				}
			}
		})
	}
}

func TestPrepareRefusesUnsupportedProjects(t *testing.T) {
	for name, mutate := range map[string]func(p *models.Project){
		"rwa-v2":         func(p *models.Project) { p.Version = "rwa-v2" },
		"no distributor": func(p *models.Project) { p.Addresses.IncomeDistributor = "" },
		"not active":     func(p *models.Project) { p.Status = models.ProjectStatusVerifying },
		"failed":         func(p *models.Project) { p.Status = models.ProjectStatusFailed },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, mutate)
			if _, err := e.svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrIncomeUnsupported) {
				t.Fatalf("err = %v, want ErrIncomeUnsupported", err)
			}
		})
	}
	t.Run("no project", func(t *testing.T) {
		svc := New(testChain, memory.New(), ipfs.NewFakeClient(), 12)
		svc.now = func() time.Time { return fixedNow }
		if _, err := svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrIncomeUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPrepareFinalityNeedsACheckpointThatIsNotFrozen(t *testing.T) {
	e := newEnv(t, nil)
	cp, _ := e.repos.IndexerCheckpoints.Get(context.Background(), testChain, indexer.CheckpointAddress)
	cp.ReconciliationRequired = true
	_ = e.repos.IndexerCheckpoints.Set(context.Background(), cp)
	if _, err := e.svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrRecordBlockNotFinal) {
		t.Errorf("frozen indexer: %v", err)
	}

	fresh := New(testChain, memory.New(), ipfs.NewFakeClient(), 12)
	_ = fresh.projects.Upsert(context.Background(), &models.Project{Version: "rwa-v3", Status: models.ProjectStatusActive, Addresses: models.Addresses{IncomeDistributor: distribA.Hex()}})
	fresh.now = func() time.Time { return fixedNow }
	if _, err := fresh.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrRecordBlockNotFinal) {
		t.Errorf("no checkpoint: %v", err)
	}
}

func TestPrepareChainHeadBelowFinalityDepth(t *testing.T) {
	e := newEnv(t, nil)
	_ = e.repos.IndexerCheckpoints.Set(context.Background(), &models.IndexerCheckpoint{ChainID: testChain, Address: indexer.CheckpointAddress, LastBlock: 5})
	req := validRequest()
	req.RecordBlock = 0
	// 5 < finality 12: the unsigned subtraction must not wrap into a huge allowed block
	if _, err := e.svc.Prepare(context.Background(), req); !errors.Is(err, ErrRecordBlockNotFinal) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareIPFSFailures(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.pinner = failingPinner{}
	if _, err := e.svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrIPFSUnavailable) {
		t.Fatalf("pin failure: %v", err)
	}
	e.svc.pinner = wrongCIDPinner{}
	if _, err := e.svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrIPFSUnavailable) {
		t.Fatalf("CID mismatch: %v", err)
	}
	e.svc.pinner = nil
	if _, err := e.svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrIPFSUnavailable) {
		t.Fatalf("no node: %v", err)
	}
	if list, _ := e.repos.IncomePreparations.List(context.Background()); len(list) != 0 {
		t.Errorf("a snapshot that was never pinned was stored (%d)", len(list))
	}
}

func TestPrepareDetectsIncompleteTransferHistory(t *testing.T) {
	e := newEnv(t, nil)
	// a mint of 5 the Transfer history does not show
	if err := e.repos.ChainEvents.Create(context.Background(), ev(controllerA, supplyEv("Minted", 9, "5"))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Prepare(context.Background(), validRequest()); !errors.Is(err, ErrIncompleteHistory) {
		t.Fatalf("err = %v, want ErrIncompleteHistory", err)
	}
}

func TestPrepareAttributesPendingRedemptions(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	for _, x := range []*models.ChainEvent{
		ev(tokenA, transferEv(25, 0, alice, escrowA, "100")),
		ev(escrowA, requested(25, "1", alice, "100")),
	} {
		if err := e.repos.ChainEvents.Create(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	prep, err := e.svc.Prepare(ctx, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseSnapshot(prep.SnapshotRaw)
	if err != nil {
		t.Fatal(err)
	}
	// alice holds 150 (300 - 50 - 100) plus 100 escrowed = 250; bob 100; carol 50; 400 eligible
	if got := doc.entries[strings.ToLower(alice.Hex())].amount; got.Int64() != 625 {
		t.Errorf("alice amount = %v, want 625 (250 of 400 eligible, total 1000)", got)
	}
}

func distributionCreated(block uint64, idx uint, id string, prep *models.IncomePreparation, deadline string) *models.ChainEvent {
	return ev(distribA, &models.ChainEvent{
		Name: "DistributionCreated", BlockNumber: block, LogIndex: idx, TxHash: fmt.Sprintf("0xd%d_%d", block, idx),
		Data: map[string]any{
			"id": id, "merkleRoot": prep.MerkleRoot, "total": prep.Total, "recordBlock": "30", "claimDeadline": deadline,
			"snapshotURI": prep.SnapshotURI, "snapshotSha256": prep.SnapshotSha256, "caller": alice.Hex(),
		},
	})
}

func claimedEv(block uint64, id string, account common.Address, amount string) *models.ChainEvent {
	return ev(distribA, &models.ChainEvent{
		Name: "IncomeClaimed", BlockNumber: block, TxHash: fmt.Sprintf("0xc%d", block),
		Data: map[string]any{"id": id, "account": account.Hex(), "amount": amount},
	})
}

func deadlineStr(d time.Duration) string { return fmt.Sprint(fixedNow.Add(d).Unix()) }

func mustCreate(t *testing.T, e *env, evs ...*models.ChainEvent) {
	t.Helper()
	for _, x := range evs {
		if err := e.repos.ChainEvents.Create(context.Background(), x); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListMatchesPreparationsToCreationEvents(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	first, err := e.svc.Prepare(ctx, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	// a later preparation of other figures, never created on-chain
	e.svc.now = func() time.Time { return fixedNow.Add(time.Minute) }
	req := validRequest()
	req.Total = "999"
	second, err := e.svc.Prepare(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	views, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Prep.ID != second.ID || views[1].Prep.ID != first.ID {
		t.Fatalf("order = %v, want newest first", []string{views[0].Prep.ID, views[1].Prep.ID})
	}
	if views[0].Status != StatusPrepared || views[1].Status != StatusPrepared {
		t.Errorf("statuses before any event: %s %s", views[0].Status, views[1].Status)
	}

	mustCreate(t, e, distributionCreated(40, 0, "0", first, deadlineStr(8*24*time.Hour)))
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	views, _ = e.svc.List(ctx)
	if views[1].Status != StatusActive || views[1].Dist == nil || views[1].Dist.ID != "0" {
		t.Errorf("created preparation: status=%s dist=%+v", views[1].Status, views[1].Dist)
	}
	if views[0].Status != StatusPrepared || views[0].Dist != nil {
		t.Errorf("a preparation with different figures matched an event: %+v", views[0])
	}
}

// The contract accepts the same root twice and each creation pays it again, so the repeat
// must stay visible to the admin instead of hiding behind the one preparation
func TestListShowsEveryCreationOfTheSameSnapshot(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	prep, _ := e.svc.Prepare(ctx, validRequest())
	mustCreate(t, e,
		distributionCreated(40, 0, "0", prep, deadlineStr(8*24*time.Hour)),
		distributionCreated(41, 0, "1", prep, deadlineStr(9*24*time.Hour)),
	)
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	views, _ := e.svc.List(ctx)
	if len(views) != 2 || views[0].Dist == nil || views[1].Dist == nil {
		t.Fatalf("want one row per creation: %+v", views)
	}
	if views[0].Dist.ID != "1" || views[1].Dist.ID != "0" || views[0].Prep.ID != prep.ID || views[1].Prep.ID != prep.ID {
		t.Errorf("rows = %s, %s; want the repeat (1) first, then the original (0), both on the one preparation",
			views[0].Dist.ID, views[1].Dist.ID)
	}
}

func TestPrepareSameSnapshotKeepsOnePreparationWithTheLatestDeadline(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	first, err := e.svc.Prepare(ctx, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	req := validRequest()
	req.ClaimDeadline += 86400
	second, err := e.svc.Prepare(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.ClaimDeadline != req.ClaimDeadline {
		t.Fatalf("second prepare = %s/%d, want %s/%d", second.ID, second.ClaimDeadline, first.ID, req.ClaimDeadline)
	}
	preps, _ := e.repos.IncomePreparations.List(ctx)
	if len(preps) != 1 || preps[0].ClaimDeadline != req.ClaimDeadline {
		t.Fatalf("stored preparations = %+v, want one with the new deadline", preps)
	}

	mustCreate(t, e, distributionCreated(40, 0, "0", first, deadlineStr(8*24*time.Hour)))
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Prepare(ctx, req); !errors.Is(err, ErrAlreadyDistributed) {
		t.Fatalf("prepare after creation: err = %v, want ErrAlreadyDistributed", err)
	}
	views, _ := e.svc.List(ctx)
	if len(views) != 1 || views[0].Status != StatusActive {
		t.Fatalf("views = %+v, want one Active", views)
	}
}

func TestListStatusFollowsDeadlineReclaimAndExtension(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	prep, _ := e.svc.Prepare(ctx, validRequest())

	// the deadline passed one second ago
	mustCreate(t, e, distributionCreated(40, 0, "0", prep, deadlineStr(-time.Second)))
	_ = e.svc.Reconcile(ctx)
	if v, _ := e.svc.List(ctx); v[0].Status != StatusExpired {
		t.Errorf("past deadline: %s", v[0].Status)
	}

	// claims are accepted through the deadline second itself
	mustCreate(t, e, distributionCreated(41, 0, "1", prep, deadlineStr(0)))
	_ = e.svc.Reconcile(ctx)
	d := &models.IncomeDistribution{ClaimDeadline: fixedNow.Unix()}
	if got := statusOf(d, fixedNow); got != StatusActive {
		t.Errorf("deadline second itself: %s", got)
	}
	if got := statusOf(d, fixedNow.Add(time.Second)); got != StatusExpired {
		t.Errorf("one second after: %s", got)
	}

	// an extension reopens it; a reclaim closes it for good
	mustCreate(t, e, ev(distribA, &models.ChainEvent{Name: "ClaimDeadlineExtended", BlockNumber: 42, TxHash: "0xe",
		Data: map[string]any{"id": "0", "previousDeadline": deadlineStr(-time.Second), "newDeadline": deadlineStr(24 * time.Hour)}}))
	_ = e.svc.Reconcile(ctx)
	v := viewOf(t, e, "0")
	if v.Status != StatusActive || v.Dist.ClaimDeadline != fixedNow.Add(24*time.Hour).Unix() {
		t.Errorf("extended: status=%s deadline=%d", v.Status, v.Dist.ClaimDeadline)
	}
	mustCreate(t, e, ev(distribA, &models.ChainEvent{Name: "UnclaimedReclaimed", BlockNumber: 43, TxHash: "0xf",
		Data: map[string]any{"id": "0", "treasury": bob.Hex(), "amount": "250"}}))
	_ = e.svc.Reconcile(ctx)
	v = viewOf(t, e, "0")
	if v.Status != StatusReclaimed || v.Dist.ReclaimedAmount != "250" {
		t.Errorf("reclaimed: status=%s amount=%s", v.Status, v.Dist.ReclaimedAmount)
	}
}

// viewOf returns List's row for on-chain distribution id
func viewOf(t *testing.T, e *env, id string) DistributionView {
	t.Helper()
	views, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Dist != nil && v.Dist.ID == id {
			return v
		}
	}
	t.Fatalf("no row for distribution %s in %+v", id, views)
	return DistributionView{}
}

func TestClaimsForServesProofsAndStatuses(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	prep, _ := e.svc.Prepare(ctx, validRequest())
	mustCreate(t, e, distributionCreated(40, 0, "0", prep, deadlineStr(8*24*time.Hour)))
	// a second distribution of the same snapshot whose deadline has passed
	mustCreate(t, e, distributionCreated(41, 0, "1", prep, deadlineStr(-time.Hour)))
	mustCreate(t, e, claimedEv(45, "0", alice, "625"))
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.ClaimsFor(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Dist.ID != "1" || got[1].Dist.ID != "0" {
		t.Fatalf("want distributions 1 then 0 (newest first), got %+v", got)
	}
	if got[0].Status != ClaimExpired || got[1].Status != ClaimClaimed || got[1].ClaimTxHash != "0xc45" {
		t.Errorf("statuses = %s / %s (tx %s)", got[0].Status, got[1].Status, got[1].ClaimTxHash)
	}
	if got[0].Amount.Int64() != 625 {
		t.Errorf("amount = %v", got[0].Amount)
	}
	root, err := parseRoot(prep.MerkleRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if !VerifyProof(root, LeafHash(c.Account, c.Amount), c.Proof) {
			t.Errorf("proof for distribution %s does not verify against the on-chain root", c.Dist.ID)
		}
	}

	// bob never claimed: the open distribution is Claimable
	bobClaims, _ := e.svc.ClaimsFor(ctx, bob)
	if len(bobClaims) != 2 || bobClaims[1].Status != ClaimClaimable || bobClaims[0].Status != ClaimExpired {
		t.Errorf("bob: %+v", bobClaims)
	}
	// carol is in the snapshot; someone who is not gets nothing
	if c, _ := e.svc.ClaimsFor(ctx, carol); len(c) != 2 {
		t.Errorf("carol: %d claims", len(c))
	}
	if c, err := e.svc.ClaimsFor(ctx, common.HexToAddress("0x1234")); err != nil || len(c) != 0 {
		t.Errorf("stranger: %v %v", c, err)
	}
}

func TestClaimsForReclaimedWithoutClaimIsExpired(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	prep, _ := e.svc.Prepare(ctx, validRequest())
	mustCreate(t, e, distributionCreated(40, 0, "0", prep, deadlineStr(8*24*time.Hour)),
		ev(distribA, &models.ChainEvent{Name: "UnclaimedReclaimed", BlockNumber: 50, TxHash: "0xrc",
			Data: map[string]any{"id": "0", "treasury": bob.Hex(), "amount": "1000"}}))
	_ = e.svc.Reconcile(ctx)
	got, _ := e.svc.ClaimsFor(ctx, bob)
	if len(got) != 1 || got[0].Status != ClaimExpired {
		t.Fatalf("a reclaimed, unclaimed entitlement must read Expired: %+v", got)
	}
}

func TestClaimsForSkipsDistributionsWithoutAServerSnapshot(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	foreign := &models.IncomePreparation{MerkleRoot: "0x" + strings.Repeat("ab", 32), SnapshotSha256: "0x" + strings.Repeat("cd", 32), Total: "1", SnapshotURI: "ipfs://x"}
	mustCreate(t, e, distributionCreated(40, 0, "0", foreign, deadlineStr(8*24*time.Hour)))
	_ = e.svc.Reconcile(ctx)
	if got, err := e.svc.ClaimsFor(ctx, alice); err != nil || len(got) != 0 {
		t.Errorf("got %v err=%v", got, err)
	}
}

func TestBuildDistributionsFoldsEventsInOrder(t *testing.T) {
	prep := &models.IncomePreparation{MerkleRoot: "0xaa", SnapshotSha256: "0xbb", Total: "1000", SnapshotURI: "ipfs://c"}
	created := distributionCreated(10, 0, "7", prep, "5000")
	events := []*models.ChainEvent{
		// supplied out of order on purpose
		claimedEv(12, "7", alice, "300"),
		claimedEv(13, "7", bob, "200"),
		ev(distribA, &models.ChainEvent{Name: "ClaimDeadlineExtended", BlockNumber: 14, Data: map[string]any{"id": "7", "newDeadline": "9000"}}),
		created,
		// events for an id that was never created, and a removed claim
		claimedEv(15, "99", carol, "1"),
		func() *models.ChainEvent { c := claimedEv(16, "7", carol, "500"); c.Removed = true; return c }(),
	}
	dists, claims := BuildDistributions(events)
	d := dists["7"]
	if d == nil || len(dists) != 1 {
		t.Fatalf("dists = %+v", dists)
	}
	if d.Claimed != "500" || d.ClaimDeadline != 9000 || d.Total != "1000" || d.Reclaimed {
		t.Errorf("fold: %+v", d)
	}
	if len(claims) != 2 {
		t.Fatalf("claims = %+v", claims)
	}
	if claims[0].ID != "7:"+strings.ToLower(alice.Hex()) || claims[0].Amount != "300" {
		t.Errorf("first claim: %+v", claims[0])
	}
}

func TestReconcileDropsRolledBackRows(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	prep, _ := e.svc.Prepare(ctx, validRequest())
	created := distributionCreated(40, 0, "0", prep, deadlineStr(8*24*time.Hour))
	mustCreate(t, e, created, claimedEv(45, "0", alice, "625"))
	_ = e.svc.Reconcile(ctx)
	if d, _ := e.repos.IncomeDistributions.List(ctx); len(d) != 1 {
		t.Fatalf("distributions = %d", len(d))
	}

	// a reorg rolls the indexer back past the creation block
	if _, err := e.repos.ChainEvents.DeleteFromBlock(ctx, testChain, distribA.Hex(), 40); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.repos.IncomeDistributions.List(ctx); len(d) != 0 {
		t.Errorf("rolled-back distribution survived: %+v", d)
	}
	if c, _ := e.repos.IncomeClaims.ListByAccount(ctx, alice.Hex()); len(c) != 0 {
		t.Errorf("rolled-back claim survived: %+v", c)
	}
	// the prepared snapshot stays and reads Prepared again
	if v, _ := e.svc.List(ctx); len(v) != 1 || v[0].Status != StatusPrepared {
		t.Errorf("after rollback: %+v", v)
	}
}

func TestReconcileIgnoresProjectsWithoutDistributor(t *testing.T) {
	e := newEnv(t, func(p *models.Project) { p.Addresses.IncomeDistributor = ""; p.Version = "rwa-v2" })
	if err := e.svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	empty := New(testChain, memory.New(), nil, 1)
	if err := empty.Reconcile(context.Background()); err != nil {
		t.Fatalf("no project: %v", err)
	}
}

func parseRoot(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		return out, fmt.Errorf("bad root %q", s)
	}
	copy(out[:], b)
	return out, nil
}

// pinCounter counts pin attempts and pins nothing
type pinCounter struct{ calls int }

func (p *pinCounter) AddRaw(context.Context, []byte) (string, error) {
	p.calls++
	return "", errors.New("pinCounter pins nothing")
}

func TestPrepare_SnapshotOverOneMiB_IsRefusedBeforePinningOrStoring(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	// 12,000 one-unit holders at about 110 canonical bytes each make roughly 1.3 MB
	const holders = 12000
	mustCreate(t, e,
		ev(tokenA, transferEv(2, 0, zeroA, vaultA, fmt.Sprint(holders))),
		ev(controllerA, supplyEv("Minted", 2, fmt.Sprint(holders))))
	for i := 0; i < holders; i++ {
		mustCreate(t, e, ev(tokenA, transferEv(3, uint(i), vaultA, common.BigToAddress(big.NewInt(int64(0x100000+i))), "1")))
	}
	pins := &pinCounter{}
	e.svc.pinner = pins
	req := validRequest()
	req.Total = "1" + strings.Repeat("0", 30)

	_, err := e.svc.Prepare(ctx, req)
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("Prepare(12,000 holders) error = %v, want ErrSnapshotTooLarge", err)
	}
	if pins.calls != 0 {
		t.Errorf("an oversized snapshot was sent to IPFS %d times", pins.calls)
	}
	if list, _ := e.repos.IncomePreparations.List(ctx); len(list) != 0 {
		t.Errorf("an oversized snapshot stored %d preparations", len(list))
	}
}

// racingPreps stores a rival preparation of the same snapshot just before Prepare's own
// Create, as a concurrent POST that passed the same duplicate check would
type racingPreps struct {
	repository.IncomePreparationRepository
	raced bool
}

func (r *racingPreps) Create(ctx context.Context, p *models.IncomePreparation) error {
	if !r.raced {
		r.raced = true
		rival := *p
		rival.ID, rival.ClaimDeadline = "rival", p.ClaimDeadline-3600
		if err := r.IncomePreparationRepository.Create(ctx, &rival); err != nil {
			return err
		}
	}
	return r.IncomePreparationRepository.Create(ctx, p)
}

// Concurrent prepares must reuse one stored preparation to avoid offering duplicate payouts
func TestPrepare_ConcurrentPrepareOfTheSameSnapshot_ReturnsTheStoredPreparation(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	e.svc.preps = &racingPreps{IncomePreparationRepository: e.repos.IncomePreparations}
	req := validRequest()

	got, err := e.svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare after losing the race: %v", err)
	}
	if got.ID != "rival" || got.ClaimDeadline != req.ClaimDeadline {
		t.Errorf("Prepare = id %s deadline %d, want the stored rival with deadline %d", got.ID, got.ClaimDeadline, req.ClaimDeadline)
	}
	list, _ := e.repos.IncomePreparations.List(ctx)
	if len(list) != 1 || list[0].ID != "rival" || list[0].ClaimDeadline != req.ClaimDeadline {
		t.Errorf("stored preparations = %+v, want only the rival with the request's deadline", list)
	}
}

func TestDemotedProject_ServesNoDistributionsAndReconcilePrunesThem(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	prep, _ := e.svc.Prepare(ctx, validRequest())
	mustCreate(t, e, distributionCreated(40, 0, "0", prep, deadlineStr(8*24*time.Hour)), claimedEv(45, "0", alice, "625"))
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.svc.ClaimsFor(ctx, bob); len(c) != 1 {
		t.Fatalf("precondition: bob has %d claims while Active, want 1", len(c))
	}

	p, _ := e.repos.Projects.Get(ctx)
	p.Status = models.ProjectStatusUndeployed // a reorg demoted the deployment
	if err := e.repos.Projects.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}

	// nothing is served even before the next Reconcile
	if c, err := e.svc.ClaimsFor(ctx, bob); err != nil || len(c) != 0 {
		t.Errorf("ClaimsFor after demotion = %d claims, err %v; want none", len(c), err)
	}
	if v, err := e.svc.List(ctx); err != nil || len(v) != 1 || v[0].Dist != nil || v[0].Status != StatusPrepared {
		t.Errorf("List after demotion = %+v, err %v; want the preparation alone, Prepared", v, err)
	}

	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.repos.IncomeDistributions.List(ctx); len(d) != 0 {
		t.Errorf("Reconcile kept %d distributions of the demoted deployment", len(d))
	}
	if c, _ := e.repos.IncomeClaims.ListByAccount(ctx, alice.Hex()); len(c) != 0 {
		t.Errorf("Reconcile kept %d claims of the demoted deployment", len(c))
	}
}

func TestClaimsFor_UnreadableSnapshot_DoesNotHideOtherDistributions(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	good, _ := e.svc.Prepare(ctx, validRequest())
	broken := &models.IncomePreparation{
		ID: "broken", MerkleRoot: "0x" + strings.Repeat("ab", 32), SnapshotSha256: "0x" + strings.Repeat("cd", 32),
		Total: "1", SnapshotURI: "ipfs://broken", SnapshotRaw: []byte(`{"entries":[`), CreatedAt: fixedNow,
	}
	if err := e.repos.IncomePreparations.Create(ctx, broken); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, e,
		distributionCreated(40, 0, "0", good, deadlineStr(8*24*time.Hour)),
		distributionCreated(41, 0, "1", broken, deadlineStr(8*24*time.Hour)))
	if err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.ClaimsFor(ctx, bob)
	if err != nil {
		t.Fatalf("one unreadable snapshot failed the whole request: %v", err)
	}
	if len(got) != 1 || got[0].Dist.ID != "0" || got[0].Status != ClaimClaimable {
		t.Errorf("ClaimsFor(bob) = %+v, want distribution 0 Claimable", got)
	}
}

// A deadline above int64, such as type(uint64).max for "never", must not wrap negative and
// read as expired while the contract still accepts claims
func TestDistributions_DeadlineAboveInt64_StaysOpen(t *testing.T) {
	const uint64Max = "18446744073709551615"
	cases := []struct {
		name   string
		events func(prep *models.IncomePreparation) []*models.ChainEvent
	}{
		{"created with uint64 max", func(prep *models.IncomePreparation) []*models.ChainEvent {
			return []*models.ChainEvent{distributionCreated(40, 0, "0", prep, uint64Max)}
		}},
		{"extended to uint64 max", func(prep *models.IncomePreparation) []*models.ChainEvent {
			return []*models.ChainEvent{
				distributionCreated(40, 0, "0", prep, deadlineStr(8*24*time.Hour)),
				ev(distribA, &models.ChainEvent{Name: "ClaimDeadlineExtended", BlockNumber: 41, TxHash: "0xx",
					Data: map[string]any{"id": "0", "previousDeadline": deadlineStr(8 * 24 * time.Hour), "newDeadline": uint64Max}}),
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			ctx := context.Background()
			prep, _ := e.svc.Prepare(ctx, validRequest())
			mustCreate(t, e, tc.events(prep)...)
			if err := e.svc.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if v := viewOf(t, e, "0"); v.Status != StatusActive || v.Dist.ClaimDeadline != math.MaxInt64 {
				t.Errorf("List row = %s, deadline %d; want Active, %d", v.Status, v.Dist.ClaimDeadline, int64(math.MaxInt64))
			}
			if c, _ := e.svc.ClaimsFor(ctx, bob); len(c) != 1 || c[0].Status != ClaimClaimable {
				t.Errorf("ClaimsFor(bob) = %+v, want one Claimable", c)
			}
		})
	}
}
