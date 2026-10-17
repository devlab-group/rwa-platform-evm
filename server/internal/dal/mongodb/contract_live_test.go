package mongodb

import (
	"context"
	"testing"

	"github.com/rwa-platform/server/internal/dal/repotest"
)

// TestLiveRepositoryContract runs the suite the memory adapter passes against MongoDB
func TestLiveRepositoryContract(t *testing.T) {
	db := liveDB(t)
	if err := EnsureIndexes(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	repotest.Run(t, New(db))
}
