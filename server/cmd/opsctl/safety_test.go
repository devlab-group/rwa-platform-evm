package main

import (
	"context"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/indexer"
)

// TestRefuseIfIndexerUnsafe covers the "refuse chain mutations while the
// indexer is paused" gate: a ReconciliationRequired checkpoint blocks a
// mutating command unless --allow-unsafe is passed.
func TestRefuseIfIndexerUnsafe(t *testing.T) {
	repos := newTestRepos()
	cfg := config.Config{ChainID: 31337}
	o := testOpsCtx(repos)
	ctx := context.Background()

	// No checkpoint yet: not unsafe.
	if err := refuseIfIndexerUnsafe(o, cfg, false); err != nil {
		t.Fatalf("no checkpoint should not be unsafe: %v", err)
	}

	if err := repos.IndexerCheckpoints.Set(ctx, &models.IndexerCheckpoint{
		ChainID: 31337, Address: indexer.CheckpointAddress, LastBlock: 42,
		LastBlockHash: "0xabc", ReconciliationRequired: true, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := refuseIfIndexerUnsafe(o, cfg, false); err == nil {
		t.Fatal("expected a paused (ReconciliationRequired) indexer to refuse the mutation")
	}
	// --allow-unsafe overrides.
	if err := refuseIfIndexerUnsafe(o, cfg, true); err != nil {
		t.Fatalf("--allow-unsafe must override the safety gate: %v", err)
	}
}
