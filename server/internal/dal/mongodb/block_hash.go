package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// blockHashDoc is the indexer_block_hashes collection's document shape
// (see repository.BlockHashRepository's doc comment).
type blockHashDoc struct {
	ChainID     int64  `bson:"chainId"`
	BlockNumber uint64 `bson:"blockNumber"`
	Hash        string `bson:"hash"`
}

type blockHashRepo struct{ coll *mongo.Collection }

func blockHashFilter(chainID int64, blockNumber uint64) bson.M {
	return bson.M{"chainId": chainID, "blockNumber": blockNumber}
}

func (r *blockHashRepo) Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error {
	_, err := r.coll.ReplaceOne(ctx, blockHashFilter(chainID, blockNumber),
		blockHashDoc{ChainID: chainID, BlockNumber: blockNumber, Hash: hash}, options.Replace().SetUpsert(true))
	return err
}

func (r *blockHashRepo) Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error) {
	doc, err := findOne[blockHashDoc](ctx, r.coll, blockHashFilter(chainID, blockNumber))
	if err != nil {
		return "", err
	}
	return doc.Hash, nil
}

func (r *blockHashRepo) PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"chainId": chainID, "blockNumber": bson.M{"$lt": keepFrom}})
	return err
}
