package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// TestLiveIncomeRepositoriesContract mirrors the memory test: preparations are created once
// and listed oldest first, distributions come back in creation order, claims are found by
// account case-insensitively, and the generation swap prunes rows a rebuild no longer wrote
func TestLiveIncomeRepositoriesContract(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	if err := EnsureIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	preps := &incomePreparationRepo{coll: db.Collection(collIncomePreparations)}
	dists := &incomeDistributionRepo{coll: db.Collection(collIncomeDistributions)}
	claims := &incomeClaimRepo{coll: db.Collection(collIncomeClaims)}

	base := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	raw := []byte(`{"formatVersion":"1"}`)
	for i, id := range []string{"c", "a", "b"} {
		p := &models.IncomePreparation{ID: id, MerkleRoot: "0xroot" + id, Total: "10", SnapshotRaw: raw, CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := preps.Create(ctx, p); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := preps.Create(ctx, &models.IncomePreparation{ID: "a"}); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate id: err = %v, want ErrAlreadyExists", err)
	}
	// one preparation per snapshot: a new id with a taken (merkleRoot, snapshotSha256) is refused
	if err := preps.Create(ctx, &models.IncomePreparation{ID: "d", MerkleRoot: "0xroota", CreatedAt: base}); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate snapshot: err = %v, want ErrAlreadyExists", err)
	}
	list, err := preps.List(ctx)
	if err != nil || len(list) != 3 || list[0].ID != "c" || list[1].ID != "a" || list[2].ID != "b" {
		t.Fatalf("List = %v, %v; want oldest first c, a, b", list, err)
	}
	for _, l := range list {
		if l.SnapshotRaw != nil {
			t.Errorf("List returned snapshotRaw for %s", l.ID)
		}
	}
	if err := preps.SetClaimDeadline(ctx, "a", 777); err != nil {
		t.Fatalf("SetClaimDeadline: %v", err)
	}
	if err := preps.SetClaimDeadline(ctx, "nope", 1); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("SetClaimDeadline unknown: err = %v", err)
	}
	got, err := preps.Get(ctx, "a")
	if err != nil || string(got.SnapshotRaw) != string(raw) || got.ClaimDeadline != 777 || !got.CreatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := preps.Get(ctx, "nope"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Get unknown: err = %v", err)
	}

	for _, d := range []*models.IncomeDistribution{
		{ID: "1", CreatedBlock: 20, CreatedLogIndex: 1, Generation: 1},
		{ID: "0", CreatedBlock: 20, CreatedLogIndex: 0, Generation: 1},
		{ID: "2", CreatedBlock: 5, CreatedLogIndex: 9, Generation: 1},
		{ID: "stale", CreatedBlock: 1, Generation: 0},
	} {
		if err := dists.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := dists.DeleteStaleGeneration(ctx, 1); err != nil {
		t.Fatal(err)
	}
	dl, _ := dists.List(ctx)
	if len(dl) != 3 || dl[0].ID != "2" || dl[1].ID != "0" || dl[2].ID != "1" {
		t.Fatalf("distributions = %+v, want creation order 2, 0, 1 with the stale row pruned", dl)
	}

	for _, c := range []*models.IncomeClaim{
		{ID: "0:0xabc", DistributionID: "0", Account: "0xabc", Amount: "5", Generation: 1},
		{ID: "1:0xabc", DistributionID: "1", Account: "0xabc", Amount: "6", Generation: 1},
		{ID: "0:0xdef", DistributionID: "0", Account: "0xdef", Amount: "7", Generation: 1},
		{ID: "9:0xabc", DistributionID: "9", Account: "0xabc", Amount: "1", Generation: 0},
	} {
		if err := claims.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := claims.DeleteStaleGeneration(ctx, 1); err != nil {
		t.Fatal(err)
	}
	cl, err := claims.ListByAccount(ctx, "0xABC")
	if err != nil || len(cl) != 2 || cl[0].ID != "0:0xabc" || cl[1].ID != "1:0xabc" {
		t.Fatalf("ListByAccount = %+v, %v", cl, err)
	}
	if cl, _ := claims.ListByAccount(ctx, "0xabc.*"); len(cl) != 0 {
		t.Errorf("a regex metacharacter in the account matched %d rows", len(cl))
	}
}

// ListAll leaves excluded event names out at the query, which txindex uses to skip the
// token's Transfer logs
func TestLiveChainEventListAllExcludesNames(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	repo := &chainEventRepo{coll: db.Collection(collChainEvents)}
	for i, name := range []string{"Transfer", "Purchased", "Transfer"} {
		e := &models.ChainEvent{ChainID: 777, Address: "0xt", TxHash: "0xh", LogIndex: uint(i), Name: name}
		if err := repo.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := repo.ListAll(ctx, 777)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListAll = %d events, %v", len(all), err)
	}
	kept, err := repo.ListAll(ctx, 777, "Transfer")
	if err != nil || len(kept) != 1 || kept[0].Name != "Purchased" {
		t.Errorf("ListAll excluding Transfer = %+v, %v", kept, err)
	}
}
