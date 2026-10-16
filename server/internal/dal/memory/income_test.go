package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// TestIncomeRepositoriesContract is the memory twin of mongodb's live contract test
func TestIncomeRepositoriesContract(t *testing.T) {
	ctx := context.Background()
	repos := New()
	preps, dists, claims := repos.IncomePreparations, repos.IncomeDistributions, repos.IncomeClaims

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
	list, _ := preps.List(ctx)
	if len(list) != 3 || list[0].ID != "c" || list[1].ID != "a" || list[2].ID != "b" {
		t.Fatalf("List must be oldest first, got %+v", list)
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
	if err != nil || string(got.SnapshotRaw) != string(raw) || got.ClaimDeadline != 777 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	// stored rows are copies: mutating a returned row must not change the store
	got.Total = "changed"
	if again, _ := preps.Get(ctx, "a"); again.Total != "10" {
		t.Errorf("a returned row aliases the stored one")
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
		_ = dists.Upsert(ctx, d)
	}
	_ = dists.DeleteStaleGeneration(ctx, 1)
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
		_ = claims.Upsert(ctx, c)
	}
	_ = claims.DeleteStaleGeneration(ctx, 1)
	cl, _ := claims.ListByAccount(ctx, "0xABC")
	if len(cl) != 2 || cl[0].ID != "0:0xabc" || cl[1].ID != "1:0xabc" {
		t.Fatalf("ListByAccount = %+v", cl)
	}
}
