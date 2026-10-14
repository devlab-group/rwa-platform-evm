// Command reindex drops chain_events for the project's contracts,
// indexer_checkpoints, indexer_block_hashes, purchases and redemption_requests.
// The running platform rebuilds them through its reconcile loops; this command
// does not run those loops. Asset profiles, investor OwnershipVerified flags,
// audit logs and idempotency records remain intact.
//
// Use it for a recovery rehearsal or after restoring read models that may lag
// the chain. server/ops/reindex_rehearsal.sh compares collection counts before
// and after; internal/indexer's TestReindexRehearsalReconstructsIdenticalReadModel
// tests reconstruction on a fake chain.
//
// Usage: go run ./cmd/reindex --config <path> [--yes]
//
// Reads the platform's YAML configuration and asks for confirmation before
// dropping read models unless --yes is set.
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

	// The deployed addresses live in the Project record, not config. The
	// project has six contracts that emit indexed events (the token and strategy
	// included), so drop chain_events for all of them. Events from the factory
	// are not dropped.
	p, err := repos.Projects.Get(ctx)
	if err != nil {
		log.Fatalf("reindex: loading project record (contract addresses come from the database, not config): %v", err)
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
	// The per-block hash history from before the reindex does not match the
	// rebuilt read model
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
	fmt.Println("the configured start block. Use server/ops/reindex_rehearsal.sh to wait for and verify that.")
}

// snapshotCounts returns a before and after document count for the operator.
// server/ops/reindex_rehearsal.sh waits for and compares the counts against a
// running platform server.
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
