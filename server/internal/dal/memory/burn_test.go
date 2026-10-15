package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// TestBurnRepositoryContract mirrors the live MongoDB burn test
func TestBurnRepositoryContract(t *testing.T) {
	ctx := context.Background()
	repo := NewBurnRepository()

	base := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	mk := func(id, rec string, i int) *models.Burn {
		return &models.Burn{BurnID: id, RecordID: rec, OperationID: "0xab" + id, Amount: "1", CreatedAt: base.Add(time.Duration(i) * time.Minute)}
	}
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		rec := "r1"
		if i == 4 {
			rec = "r2"
		}
		if err := repo.Create(ctx, mk(id, rec, i)); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	dup := mk("z", "r1", 9)
	dup.OperationID = "0xaba" // same operationId as burn a
	if err := repo.Create(ctx, dup); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate operationId: err = %v, want ErrAlreadyExists", err)
	}
	if err := repo.Create(ctx, mk("a", "r1", 0)); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("duplicate burnId: err = %v, want ErrAlreadyExists", err)
	}
	if got, err := repo.GetByOperationID(ctx, "0xABC"); err != nil || got.BurnID != "c" {
		t.Errorf("GetByOperationID = %v, %v", got, err)
	}
	if _, err := repo.GetByOperationID(ctx, "0xnone"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("GetByOperationID unknown: err = %v", err)
	}
	if got, _ := repo.ListByRecord(ctx, "r1"); len(got) != 4 {
		t.Errorf("ListByRecord r1 = %d, want 4", len(got))
	}

	b, _ := repo.Get(ctx, "a")
	b.Status = models.BurnStatusBurned
	if ok, err := repo.UpdateConditional(ctx, b, 0); err != nil || !ok {
		t.Fatalf("first UpdateConditional = %v, %v", ok, err)
	}
	if ok, err := repo.UpdateConditional(ctx, b, 0); err != nil || ok {
		t.Errorf("stale UpdateConditional = %v, %v, want false, nil", ok, err)
	}
	if got, _ := repo.Get(ctx, "a"); got.Version != 1 || got.Status != models.BurnStatusBurned {
		t.Errorf("after update: %+v", got)
	}
	if _, err := repo.UpdateConditional(ctx, mk("nope", "r1", 0), 0); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("UpdateConditional missing: err = %v", err)
	}

	var ids []string
	cursor := ""
	for range 10 {
		items, next, err := repo.ListPage(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			ids = append(ids, it.BurnID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if got := strings.Join(ids, ""); got != "edcba" {
		t.Errorf("ListPage order = %s, want edcba (newest first)", got)
	}
}
