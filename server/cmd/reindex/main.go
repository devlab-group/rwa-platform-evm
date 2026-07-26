// Command reindex performs a "chain reindex rehearsal": drop this
// server's own indexed event log and every read model derived purely from
// it, reset the indexer checkpoint, then let the normal background
// reconcile loops (already running in the live `platform` process this
// tool targets — reindex itself does not run them) rebuild everything from
// the chain, which remains the actual source of truth throughout.
//
// This is a REHEARSAL/RECOVERY tool, not something a running deployment
// needs day to day: run it to prove disaster-recovery works, or for real
// after a Mongo restore whose read models might be stale relative to the
// chain. It does NOT touch off-chain-authored data (Asset Profiles,
// investor OwnershipVerified flags, audit logs, idempotency records) —
// only the collections that are derived purely from indexed chain events,
// never from server-side optimistic writes: chain_events,
// indexer_checkpoints, purchases, redemption_requests.
// See internal/indexer's TestReindexRehearsalReconstructsIdenticalReadModel
// for the same rehearsal proven on a fake chain, and
// server/ops/reindex_rehearsal.sh for the operator runbook that wraps this
// tool with a before/after collection-count sanity check.
//
// Usage: go run ./cmd/reindex --config <path> [--yes]
// Reads the same --config YAML file (MongoDB/chain/contract addresses) as the
// platform binary (internal/config). Requires --yes (or a "yes" typed at
// the confirmation prompt) since this is destructive to the local read
// model, even though it is recoverable by design.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal"
	"github.com/rwa-platform/server/internal/dal/mongodb"
	"github.com/rwa-platform/server/internal/indexer"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func main() {
	yes := flag.Bool("yes", false, "skip the interactive confirmation prompt (scripted use)")
	configPath := flag.String("config", "", "path to the YAML configuration file (required)")
	flag.Parse()

	if *configPath == "" {
		log.Fatalf("reindex: --config <path> is required")
	}
	cfg, err := config.LoadFile(*configPath)
	if err != nil {
		log.Fatalf("reindex: config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := dal.Connect(ctx, cfg.MongoURI)
	if err != nil {
		log.Fatalf("reindex: connecting to MongoDB: %v", err)
	}
	defer client.Disconnect(context.Background())
	db := client.Database(cfg.MongoDB)
	repos := mongodb.New(db)

	// Config is bootstrap-only: the deployed contract addresses live in the DB
	// Project record (kept live by the indexer), not config — so load them
	// from there. The scanned set is all SIX indexed contracts (token +
	// strategy emit events too), so a rehearsal must drop every one's
	// chain_events, not just the four settlement/compliance contracts.
	p, err := repos.Projects.Get(ctx)
	if err != nil {
		log.Fatalf("reindex: loading project record (contract addresses are now sourced from the DB, not config): %v", err)
	}
	addresses := map[string]string{
		"token":            p.Addresses.Token,
		"compliance":       p.Addresses.Compliance,
		"supplyController": p.Addresses.SupplyController,
		"vault":            p.Addresses.Vault,
		"redemptionEscrow": p.Addresses.RedemptionEscrow,
		"strategy":         p.Addresses.Strategy,
	}

	fmt.Println("=== reindex rehearsal ===")
	fmt.Printf("chainId=%d mongoDb=%s\n", cfg.ChainID, cfg.MongoDB)
	for role, addr := range addresses {
		fmt.Printf("  %-16s %s\n", role, addr)
	}
	fmt.Println("This will DROP chain_events, indexer_checkpoints, purchases, and")
	fmt.Println("redemption_requests, then rely on the platform server's own background reconcile")
	fmt.Println("loops to rebuild them from the chain. Asset profiles, investor ownership flags,")
	fmt.Println("and audit logs are NOT touched.")

	if !*yes {
		fmt.Print("\nType \"yes\" to proceed: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			fmt.Println("aborted.")
			os.Exit(1)
		}
	}

	before := snapshotCounts(ctx, db)
	fmt.Printf("\nbefore: %+v\n", before)

	for role, addr := range addresses {
		if addr == "" {
			continue
		}
		n, err := repos.ChainEvents.DeleteFromBlock(ctx, cfg.ChainID, addr, 0)
		if err != nil {
			log.Fatalf("reindex: dropping chain_events for %s (%s): %v", role, addr, err)
		}
		fmt.Printf("dropped %d chain_events for %s (%s)\n", n, role, addr)
	}
	if _, err := db.Collection("indexer_checkpoints").DeleteMany(ctx, bson.M{"chainId": cfg.ChainID, "address": indexer.CheckpointAddress}); err != nil {
		log.Fatalf("reindex: resetting indexer checkpoint: %v", err)
	}
	// Also drop the retained per-block hash history for this chain —
	// stale entries from before the reindex are meaningless against a
	// freshly rebuilt read model and would otherwise just sit unused until
	// naturally overwritten.
	if _, err := db.Collection("indexer_block_hashes").DeleteMany(ctx, bson.M{"chainId": cfg.ChainID}); err != nil {
		log.Fatalf("reindex: resetting indexer block hash history: %v", err)
	}
	if err := repos.Purchases.DeleteAll(ctx); err != nil {
		log.Fatalf("reindex: dropping purchases: %v", err)
	}
	if err := repos.RedemptionRequests.DeleteAll(ctx); err != nil {
		log.Fatalf("reindex: dropping redemption_requests: %v", err)
	}

	fmt.Println("\nDone. Read models are empty; they will repopulate as the running platform")
	fmt.Println("server's background reconcile loops (5-15s tickers) re-scan the chain from")
	fmt.Println("block 0. Use server/ops/reindex_rehearsal.sh to wait for and verify that.")
}

// snapshotCounts is a simple before/after sanity signal for the operator
// running this interactively; server/ops/reindex_rehearsal.sh does the
// real wait-and-compare against a running platform server.
func snapshotCounts(ctx context.Context, db *mongo.Database) map[string]int64 {
	counts := map[string]int64{}
	for _, coll := range []string{"chain_events", "purchases", "redemption_requests"} {
		n, err := db.Collection(coll).CountDocuments(ctx, bson.M{})
		if err != nil {
			counts[coll] = -1
			continue
		}
		counts[coll] = n
	}
	return counts
}
