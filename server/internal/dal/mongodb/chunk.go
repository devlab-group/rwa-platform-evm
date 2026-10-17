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

// chainChunkRepo writes events, block hashes, pruning and a version-checked checkpoint
// in one majority-write MongoDB transaction. Transactions require a replica set or
// sharded cluster, not standalone mongod. Memory tests cover the versioning contract
// without testing MongoDB transactions.
type chainChunkRepo struct {
	db          *mongo.Database
	events      *mongo.Collection
	blockHashes *mongo.Collection
	checkpoints *mongo.Collection
}

// storedCheckpointVersion reads the transactional checkpoint version for (chainId,
// address), treating absence as initial version 0
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
		committed = false // WithTransaction can run the callback more than once, so reset per attempt

		// commit only if the stored checkpoint version still equals the one this
		// chunk was built on
		curVersion, findErr := r.storedCheckpointVersion(txCtx, c.Checkpoint.ChainID, c.Checkpoint.Address)
		if findErr != nil {
			return nil, findErr
		}
		if curVersion != c.ExpectedCheckpointVersion {
			return nil, nil // lost the race: nothing is written and committed stays false
		}

		for _, e := range c.Events {
			// Skip existing events before insert: duplicate-key errors abort the transaction
			key := models.EventKey{ChainID: e.ChainID, Address: e.Address, TxHash: e.TxHash, LogIndex: e.LogIndex}
			findErr := r.events.FindOne(txCtx, eventFilter(key)).Err()
			if findErr == nil {
				continue
			}
			if !errors.Is(findErr, mongo.ErrNoDocuments) {
				return nil, findErr
			}
			if _, insErr := r.events.InsertOne(txCtx, e); insErr != nil {
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

// RewindChunk deletes events and rewinds the checkpoint in one majority-write
// transaction. The version check prevents a racing forward commit and rewind from both
// applying; see repository.ChainChunkRepository.RewindChunk.
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
		committed = false // WithTransaction can run the callback more than once, so reset per attempt

		curVersion, findErr := r.storedCheckpointVersion(txCtx, rw.Checkpoint.ChainID, rw.Checkpoint.Address)
		if findErr != nil {
			return nil, findErr
		}
		if curVersion != rw.ExpectedCheckpointVersion {
			return nil, nil // lost the race: nothing is written and committed stays false
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
