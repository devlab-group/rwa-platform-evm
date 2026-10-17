package mongodb

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

const chunkChain int64 = 31337

// liveChunkRepo uses the live test database; a configured standalone mongod fails rather
// than skipping transaction atomicity tests
func liveChunkRepo(t *testing.T) (*chainChunkRepo, *mongo.Database) {
	t.Helper()
	db := liveDB(t)
	ctx := context.Background()
	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if hello.SetName == "" && hello.Msg != "isdbgrid" {
		t.Fatal("MONGO_TEST_URI points at a standalone mongod: CommitChunk and RewindChunk need transactions, so use a replica set (e.g. ?replicaSet=rs0&directConnection=true)")
	}
	if err := EnsureIndexes(ctx, db); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	repo := &chainChunkRepo{
		db: db, events: db.Collection(collChainEvents),
		blockHashes: db.Collection(collIndexerBlockHashes), checkpoints: db.Collection(collIndexerCheckpoints),
	}
	return repo, db
}

func chunkEvent(addr string, block uint64, name string) *models.ChainEvent {
	return &models.ChainEvent{
		ChainID: chunkChain, Address: addr, TxHash: "0xtx" + name + string(rune('a'+block%26)), LogIndex: uint(block),
		BlockNumber: block, Name: name, Data: map[string]any{"n": int64(block)},
	}
}

func checkpointAt(addr string, last uint64) *models.IndexerCheckpoint {
	return &models.IndexerCheckpoint{ChainID: chunkChain, Address: addr, LastBlock: last, LastBlockHash: "0xhash"}
}

func countDocs(t *testing.T, c *mongo.Collection, filter any) int64 {
	t.Helper()
	n, err := c.CountDocuments(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func storedVersion(t *testing.T, r *chainChunkRepo, addr string) (int, bool) {
	t.Helper()
	var cp models.IndexerCheckpoint
	err := r.checkpoints.FindOne(context.Background(), bson.M{"chainId": chunkChain, "address": addr}).Decode(&cp)
	if err == mongo.ErrNoDocuments {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return cp.Version, true
}

func TestLiveCommitChunk_WritesEventsHashesPruneAndCheckpointTogether(t *testing.T) {
	r, _ := liveChunkRepo(t)
	ctx := context.Background()
	const addr = "0xA"
	// A hash from an earlier chunk that the new chunk prunes
	if _, err := r.blockHashes.InsertOne(ctx, blockHashDoc{ChainID: chunkChain, BlockNumber: 1, Hash: "0xold"}); err != nil {
		t.Fatal(err)
	}
	prune := uint64(5)

	ok, err := r.CommitChunk(ctx, repository.ChunkCommit{
		ChainID:                chunkChain,
		Events:                 []*models.ChainEvent{chunkEvent(addr, 10, "Transfer"), chunkEvent(addr, 11, "Transfer")},
		BlockHashes:            []repository.BlockHashEntry{{BlockNumber: 10, Hash: "0xh10"}, {BlockNumber: 11, Hash: "0xh11"}},
		PruneBlockHashesBefore: &prune,
		Checkpoint:             checkpointAt(addr, 11),
	})
	if err != nil || !ok {
		t.Fatalf("CommitChunk = %v, %v, want committed", ok, err)
	}
	if n := countDocs(t, r.events, bson.M{"address": addr}); n != 2 {
		t.Errorf("events stored = %d, want 2", n)
	}
	if n := countDocs(t, r.blockHashes, bson.M{"chainId": chunkChain}); n != 2 {
		t.Errorf("block hashes stored = %d, want 2 (10 and 11; 1 was pruned)", n)
	}
	if v, found := storedVersion(t, r, addr); !found || v != 1 {
		t.Errorf("checkpoint version = %d (found %v), want 1", v, found)
	}
}

func TestLiveCommitChunk_ReingestedEventIsSkippedWithoutFailingTheChunk(t *testing.T) {
	r, _ := liveChunkRepo(t)
	ctx := context.Background()
	const addr = "0xA"
	ev := chunkEvent(addr, 10, "Transfer")
	if ok, err := r.CommitChunk(ctx, repository.ChunkCommit{ChainID: chunkChain, Events: []*models.ChainEvent{ev}, Checkpoint: checkpointAt(addr, 10)}); err != nil || !ok {
		t.Fatalf("first CommitChunk = %v, %v", ok, err)
	}
	dup := *ev
	ok, err := r.CommitChunk(ctx, repository.ChunkCommit{
		ChainID: chunkChain, Events: []*models.ChainEvent{&dup, chunkEvent(addr, 11, "Transfer")},
		Checkpoint: checkpointAt(addr, 11), ExpectedCheckpointVersion: 1,
	})
	if err != nil || !ok {
		t.Fatalf("CommitChunk with a duplicate = %v, %v, want committed", ok, err)
	}
	if n := countDocs(t, r.events, bson.M{"address": addr}); n != 2 {
		t.Errorf("events stored = %d, want 2 (the duplicate stored once)", n)
	}
}

// An event the database rejects midway through the chunk must roll back the earlier
// inserts, the block hashes and the checkpoint
func TestLiveCommitChunk_FailureMidChunkWritesNothing(t *testing.T) {
	r, db := liveChunkRepo(t)
	ctx := context.Background()
	const addr = "0xA"
	// Reject events named Poison, as a schema violation would
	if err := db.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: collChainEvents},
		{Key: "validator", Value: bson.M{"name": bson.M{"$ne": "Poison"}}},
		{Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		t.Fatalf("collMod: %v", err)
	}

	ok, err := r.CommitChunk(ctx, repository.ChunkCommit{
		ChainID:     chunkChain,
		Events:      []*models.ChainEvent{chunkEvent(addr, 10, "Transfer"), chunkEvent(addr, 11, "Transfer"), chunkEvent(addr, 12, "Poison")},
		BlockHashes: []repository.BlockHashEntry{{BlockNumber: 10, Hash: "0xh10"}},
		Checkpoint:  checkpointAt(addr, 12),
	})
	if err == nil || ok {
		t.Fatalf("CommitChunk = %v, %v, want an error and no commit", ok, err)
	}
	if n := countDocs(t, r.events, bson.M{}); n != 0 {
		t.Errorf("%d events survived the failed chunk, want 0", n)
	}
	if n := countDocs(t, r.blockHashes, bson.M{}); n != 0 {
		t.Errorf("%d block hashes survived the failed chunk, want 0", n)
	}
	if _, found := storedVersion(t, r, addr); found {
		t.Error("a checkpoint was written by the failed chunk")
	}
}

func TestLiveCommitChunk_StaleWriterIsRejectedAndWritesNothing(t *testing.T) {
	r, _ := liveChunkRepo(t)
	ctx := context.Background()
	const addr = "0xA"
	winner := repository.ChunkCommit{ChainID: chunkChain, Events: []*models.ChainEvent{chunkEvent(addr, 10, "Transfer")}, Checkpoint: checkpointAt(addr, 10)}
	loser := repository.ChunkCommit{
		ChainID: chunkChain, Events: []*models.ChainEvent{chunkEvent(addr, 10, "Other"), chunkEvent(addr, 11, "Other")},
		BlockHashes: []repository.BlockHashEntry{{BlockNumber: 10, Hash: "0xloser"}}, Checkpoint: checkpointAt(addr, 99),
	}

	if ok, err := r.CommitChunk(ctx, winner); err != nil || !ok {
		t.Fatalf("winner CommitChunk = %v, %v", ok, err)
	}
	// Both were built on version 0; the second finds version 1
	ok, err := r.CommitChunk(ctx, loser)
	if err != nil || ok {
		t.Fatalf("stale CommitChunk = %v, %v, want (false, nil)", ok, err)
	}
	if n := countDocs(t, r.events, bson.M{}); n != 1 {
		t.Errorf("events stored = %d, want only the winner's 1", n)
	}
	if n := countDocs(t, r.blockHashes, bson.M{}); n != 0 {
		t.Errorf("block hashes stored = %d, want 0", n)
	}
	var cp models.IndexerCheckpoint
	if err := r.checkpoints.FindOne(ctx, bson.M{"address": addr}).Decode(&cp); err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 10 || cp.Version != 1 {
		t.Errorf("checkpoint = block %d version %d, want the winner's 10 and 1", cp.LastBlock, cp.Version)
	}
}

func TestLiveRewindChunk_RemovesExactlyTheRolledBackRange(t *testing.T) {
	r, _ := liveChunkRepo(t)
	ctx := context.Background()
	const a, b = "0xA", "0xB"
	other := chunkEvent(a, 13, "Transfer")
	other.ChainID = chunkChain + 1
	seed := []*models.ChainEvent{
		chunkEvent(a, 10, "Transfer"), chunkEvent(a, 11, "Transfer"), chunkEvent(a, 12, "Transfer"), chunkEvent(a, 13, "Transfer"),
		chunkEvent(b, 12, "Transfer"), other,
	}
	for _, e := range seed {
		if _, err := r.events.InsertOne(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := r.CommitChunk(ctx, repository.ChunkCommit{ChainID: chunkChain, Checkpoint: checkpointAt(a, 13)}); err != nil || !ok {
		t.Fatalf("seed checkpoint: %v, %v", ok, err)
	}

	ok, err := r.RewindChunk(ctx, repository.ChunkRewind{
		ChainID: chunkChain, Addresses: []string{a}, DeleteFromBlock: 12,
		Checkpoint: checkpointAt(a, 11), ExpectedCheckpointVersion: 1,
	})
	if err != nil || !ok {
		t.Fatalf("RewindChunk = %v, %v, want committed", ok, err)
	}
	if n := countDocs(t, r.events, bson.M{"chainId": chunkChain, "address": a, "blockNumber": bson.M{"$lt": 12}}); n != 2 {
		t.Errorf("events below the rewind block = %d, want 2 kept", n)
	}
	if n := countDocs(t, r.events, bson.M{"chainId": chunkChain, "address": a, "blockNumber": bson.M{"$gte": 12}}); n != 0 {
		t.Errorf("events at or above the rewind block = %d, want 0", n)
	}
	if n := countDocs(t, r.events, bson.M{"address": b}); n != 1 {
		t.Errorf("another address lost events: %d left, want 1", n)
	}
	if n := countDocs(t, r.events, bson.M{"chainId": chunkChain + 1}); n != 1 {
		t.Errorf("another chain lost events: %d left, want 1", n)
	}
	var cp models.IndexerCheckpoint
	if err := r.checkpoints.FindOne(ctx, bson.M{"address": a}).Decode(&cp); err != nil {
		t.Fatal(err)
	}
	if cp.LastBlock != 11 || cp.Version != 2 {
		t.Errorf("checkpoint = block %d version %d, want 11 and 2", cp.LastBlock, cp.Version)
	}
}

func TestLiveRewindChunk_StaleWriterDeletesNothing(t *testing.T) {
	r, _ := liveChunkRepo(t)
	ctx := context.Background()
	const addr = "0xA"
	if ok, err := r.CommitChunk(ctx, repository.ChunkCommit{
		ChainID: chunkChain, Events: []*models.ChainEvent{chunkEvent(addr, 10, "Transfer"), chunkEvent(addr, 11, "Transfer")},
		Checkpoint: checkpointAt(addr, 11),
	}); err != nil || !ok {
		t.Fatalf("seed: %v, %v", ok, err)
	}

	ok, err := r.RewindChunk(ctx, repository.ChunkRewind{
		ChainID: chunkChain, Addresses: []string{addr}, DeleteFromBlock: 10,
		Checkpoint: checkpointAt(addr, 9), ExpectedCheckpointVersion: 0, // stale: stored version is 1
	})
	if err != nil || ok {
		t.Fatalf("stale RewindChunk = %v, %v, want (false, nil)", ok, err)
	}
	if n := countDocs(t, r.events, bson.M{}); n != 2 {
		t.Errorf("events left = %d, want both untouched", n)
	}
	if v, _ := storedVersion(t, r, addr); v != 1 {
		t.Errorf("checkpoint version = %d, want it unchanged at 1", v)
	}
}
