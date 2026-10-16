// Command reindex deletes chain_events for stored child addresses, the chain's shared
// indexer checkpoint/hash history, purchases, redemption_requests and the rwa-v3 income
// read models (income_distributions, income_claims). Factory events, Asset Profiles,
// prepared income snapshots (income_preparations), investor OwnershipVerified flags,
// audit logs and idempotency records remain.
//
// Deployments indexed before RWAToken Transfer logs were stored need one reindex before
// POST /api/v1/income/distributions can prepare snapshots.
//
// A running platform rebuilds indexed data; reindex does not scan. Use
// server/ops/reindex_rehearsal.sh for before/after counts or internal/indexer's
// TestReindexRehearsalReconstructsIdenticalReadModel for a fake-chain check.
//
// Usage: go run ./cmd/reindex --config <path> [--yes]
//
// Reads platform's YAML config and requires confirmation unless --yes is set.
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

	// Delete child events at the token, compliance, supply controller, Vault, escrow and
	// strategy addresses stored in Project.Addresses; retain factory events
	p, err := repos.Projects.Get(ctx)
	if err != nil {
		log.Fatalf("reindex: loading project record (contract addresses come from the database, not config): %v", err)
	}
	addresses := map[string]string{
		"token":             p.Addresses.Token,
		"compliance":        p.Addresses.Compliance,
		"supplyController":  p.Addresses.SupplyController,
		"vault":             p.Addresses.Vault,
		"redemptionEscrow":  p.Addresses.RedemptionEscrow,
		"strategy":          p.Addresses.Strategy,
		"incomeDistributor": p.Addresses.IncomeDistributor,
	}

	fmt.Println("=== reindex rehearsal ===")
	fmt.Printf("chainId=%d mongoDb=%s\n", cfg.ChainID, cfg.MongoDB)
	for role, addr := range addresses {
		fmt.Printf("  %-16s %s\n", role, addr)
	}
	fmt.Println("This will DROP chain_events, indexer_checkpoints, purchases,")
	fmt.Println("redemption_requests, income_distributions and income_claims, then rely on the platform server's own background reconcile")
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
	// Discard block-hash history alongside the checkpoint for the rescan
	if _, err := db.Collection("indexer_block_hashes").DeleteMany(ctx, bson.M{"chainId": cfg.ChainID}); err != nil {
		log.Fatalf("reindex: resetting indexer block hash history: %v", err)
	}
	if err := repos.Purchases.DeleteAll(ctx); err != nil {
		log.Fatalf("reindex: dropping purchases: %v", err)
	}
	if err := repos.RedemptionRequests.DeleteAll(ctx); err != nil {
		log.Fatalf("reindex: dropping redemption_requests: %v", err)
	}
	// Income read models are derived from events; prepared snapshots are not and stay
	for _, coll := range []string{"income_distributions", "income_claims"} {
		if _, err := db.Collection(coll).DeleteMany(ctx, bson.M{}); err != nil {
			log.Fatalf("reindex: dropping %s: %v", coll, err)
		}
	}

	fmt.Println("\nDone. Read models are empty; they will repopulate as the running platform")
	fmt.Println("server's background reconcile loops (5-15s tickers) re-scan the chain from")
	fmt.Println("the configured start block. Use server/ops/reindex_rehearsal.sh to wait for and verify that.")
}

// snapshotCounts reads current collection counts, using -1 on errors; the external
// rehearsal script polls before/after counts while platform rebuilds
func snapshotCounts(ctx context.Context, db *mongo.Database) map[string]int64 {
	counts := map[string]int64{}
	for _, coll := range []string{"chain_events", "purchases", "redemption_requests", "income_distributions", "income_claims"} {
		n, err := db.Collection(coll).CountDocuments(ctx, bson.M{})
		if err != nil {
			counts[coll] = -1
			continue
		}
		counts[coll] = n
	}
	return counts
}
