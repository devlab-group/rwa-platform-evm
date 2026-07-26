package mongodb

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// chainChunkRepo commits one indexer scan chunk atomically:
// validated events, retained block hashes, pruning, and the advanced
// checkpoint all land in a single Mongo transaction with majority write
// concern, and the checkpoint advance is conditional on the stored version
// still matching what the caller layered on. Before this the indexer wrote
// each of those through separate calls, so a crash or write failure
// mid-commit could leave orphaned fork events behind a still-old checkpoint.
//
// A transaction requires a replica set / mongos; a standalone mongod rejects
// it. Production deployments run a replica set, so this is
// the correct primitive there; unit tests exercise the atomic semantics
// against the interface-identical in-memory implementation instead (there is
// no local replica set in the `go test` harness).
type chainChunkRepo struct {
	db          *mongo.Database
	events      *mongo.Collection
	blockHashes *mongo.Collection
	checkpoints *mongo.Collection
}

// storedCheckpointVersion reads the current version of the (chainId,address)
// checkpoint inside a transaction. An absent checkpoint is version 0, the same
// starting point a fresh indexer layers its first commit on.
func (r *chainChunkRepo) storedCheckpointVersion(txCtx context.Context, chainID int64, address string) (int, error) {
	var cur models.IndexerCheckpoint
	err := r.checkpoints.FindOne(txCtx, bson.M{"chainId": chainID, "address": address}).Decode(&cur)
	switch {
	case err == nil:
		return cur.Version, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return 0, nil
	default:
		return 0, err
	}
}

func (r *chainChunkRepo) CommitChunk(ctx context.Context, c repository.ChunkCommit) (bool, error) {
	if c.Checkpoint == nil {
		return false, repository.ErrNotFound
	}
	session, err := r.db.Client().StartSession()
	if err != nil {
		return false, err
	}
	defer session.EndSession(ctx)

	committed := false
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		committed = false // reset on every (possibly retried) transaction attempt

		// Conditional checkpoint advance: only commit if the stored
		// checkpoint's version still equals what this chunk layered on.
		curVersion, findErr := r.storedCheckpointVersion(txCtx, c.Checkpoint.ChainID, c.Checkpoint.Address)
		if findErr != nil {
			return nil, findErr
		}
		if curVersion != c.ExpectedCheckpointVersion {
			return nil, nil // lost the race; committed stays false, nothing written
		}

		for _, e := range c.Events {
			if _, insErr := r.events.InsertOne(txCtx, e); insErr != nil {
				if mongo.IsDuplicateKeyError(insErr) {
					continue // idempotent re-ingestion, matches chainEventRepo.Create
				}
				return nil, insErr
			}
		}
		for _, bh := range c.BlockHashes {
			if _, hErr := r.blockHashes.ReplaceOne(txCtx, blockHashFilter(c.ChainID, bh.BlockNumber),
				blockHashDoc{ChainID: c.ChainID, BlockNumber: bh.BlockNumber, Hash: bh.Hash},
				options.Replace().SetUpsert(true)); hErr != nil {
				return nil, hErr
			}
		}
		if c.PruneBlockHashesBefore != nil {
			if _, pErr := r.blockHashes.DeleteMany(txCtx, bson.M{"chainId": c.ChainID, "blockNumber": bson.M{"$lt": *c.PruneBlockHashesBefore}}); pErr != nil {
				return nil, pErr
			}
		}
		cp := *c.Checkpoint
		cp.Version = c.ExpectedCheckpointVersion + 1
		if _, cErr := r.checkpoints.ReplaceOne(txCtx, bson.M{"chainId": cp.ChainID, "address": cp.Address}, &cp, options.Replace().SetUpsert(true)); cErr != nil {
			return nil, cErr
		}
		committed = true
		return nil, nil
	}, options.Transaction().SetWriteConcern(writeconcern.Majority()))
	if err != nil {
		return false, err
	}
	return committed, nil
}

// RewindChunk is CommitChunk's backwards counterpart — the delete and the
// rewound checkpoint land in one majority-write transaction under the same
// version fence, so a forward commit racing the rewind either loses the fence
// (and writes nothing) or wins it (and makes the rewind lose), never lands
// half-buried in between. See repository.ChainChunkRepository.RewindChunk.
func (r *chainChunkRepo) RewindChunk(ctx context.Context, rw repository.ChunkRewind) (bool, error) {
	if rw.Checkpoint == nil {
		return false, repository.ErrNotFound
	}
	session, err := r.db.Client().StartSession()
	if err != nil {
		return false, err
	}
	defer session.EndSession(ctx)

	committed := false
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		committed = false // reset on every (possibly retried) transaction attempt

		curVersion, findErr := r.storedCheckpointVersion(txCtx, rw.Checkpoint.ChainID, rw.Checkpoint.Address)
		if findErr != nil {
			return nil, findErr
		}
		if curVersion != rw.ExpectedCheckpointVersion {
			return nil, nil // lost the race; committed stays false, nothing written
		}

		for _, addr := range rw.Addresses {
			if _, dErr := r.events.DeleteMany(txCtx, bson.M{
				"chainId": rw.ChainID, "address": addr,
				"blockNumber": bson.M{"$gte": rw.DeleteFromBlock},
			}); dErr != nil {
				return nil, dErr
			}
		}
		cp := *rw.Checkpoint
		cp.Version = rw.ExpectedCheckpointVersion + 1
		if _, cErr := r.checkpoints.ReplaceOne(txCtx, bson.M{"chainId": cp.ChainID, "address": cp.Address}, &cp,
			options.Replace().SetUpsert(true)); cErr != nil {
			return nil, cErr
		}
		committed = true
		return nil, nil
	}, options.Transaction().SetWriteConcern(writeconcern.Majority()))
	if err != nil {
		return false, err
	}
	return committed, nil
}
