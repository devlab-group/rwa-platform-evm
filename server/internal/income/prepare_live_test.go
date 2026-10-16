package income

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/mongodb"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// liveRepos returns MongoDB repositories on a fresh database with the production indexes,
// dropped when the test ends. It skips without MONGO_TEST_URI.
func liveRepos(t *testing.T) *repository.Repositories {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping live-Mongo income test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect %s: %v", uri, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("ping %s: %v", uri, err)
	}
	db := client.Database(fmt.Sprintf("rwa_income_live_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		bg := context.Background()
		_ = db.Drop(bg)
		_ = client.Disconnect(bg)
	})
	if err := mongodb.EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	return mongodb.New(db)
}

// listBarrier holds the first n List calls until all n have arrived, so every concurrent
// Prepare passes the duplicate check before any of them inserts
type listBarrier struct {
	repository.IncomePreparationRepository
	n       int32
	arrived atomic.Int32
	ready   chan struct{}
}

func (b *listBarrier) List(ctx context.Context) ([]*models.IncomePreparation, error) {
	if k := b.arrived.Add(1); k <= b.n {
		if k == b.n {
			close(b.ready)
		}
		select {
		case <-b.ready:
		case <-time.After(10 * time.Second):
			return nil, errors.New("listBarrier: not every Prepare reached the duplicate check")
		}
	}
	return b.IncomePreparationRepository.List(ctx)
}

// The unique (merkleRoot, snapshotSha256) index must prevent duplicate Prepared rows when
// concurrent requests pass the list check, avoiding another offered payout
func TestLivePrepare_ConcurrentPreparesOfOneSnapshot_StoreOnePreparation(t *testing.T) {
	const n = 8
	repos := liveRepos(t)
	e := newEnvWith(t, repos, nil)
	e.svc.preps = &listBarrier{IncomePreparationRepository: repos.IncomePreparations, n: n, ready: make(chan struct{})}
	ctx := context.Background()

	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := validRequest()
			req.ClaimDeadline += int64(i) // each request moves the deadline; the stored row keeps one of them
			p, err := e.svc.Prepare(ctx, req)
			errs[i] = err
			if p != nil {
				ids[i] = p.ID
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Prepare #%d: %v", i, err)
		}
	}
	stored, err := repos.IncomePreparations.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d preparations of one snapshot, want 1", len(stored))
	}
	for i, id := range ids {
		if id != stored[0].ID {
			t.Errorf("Prepare #%d returned preparation %q, want the stored %q", i, id, stored[0].ID)
		}
	}
	base := validRequest().ClaimDeadline
	if d := stored[0].ClaimDeadline; d < base || d >= base+n {
		t.Errorf("stored deadline %d is none of the requested %d..%d", d, base, base+n-1)
	}
}
