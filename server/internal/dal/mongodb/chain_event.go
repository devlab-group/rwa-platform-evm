package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type chainEventRepo struct{ coll *mongo.Collection }

func eventFilter(k models.EventKey) bson.M {
	return bson.M{"chainId": k.ChainID, "address": k.Address, "txHash": k.TxHash, "logIndex": k.LogIndex}
}

func (r *chainEventRepo) Exists(ctx context.Context, key models.EventKey) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, eventFilter(key))
	return n > 0, err
}

func (r *chainEventRepo) Create(ctx context.Context, e *models.ChainEvent) error {
	err := createDoc(ctx, r.coll, e)
	if err == repository.ErrAlreadyExists {
		return nil // idempotent: matches memory repo semantics (re-ingestion is a no-op)
	}
	return err
}

func (r *chainEventRepo) DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error) {
	res, err := r.coll.DeleteMany(ctx, bson.M{"chainId": chainID, "address": address, "blockNumber": bson.M{"$gte": fromBlock}})
	if err != nil {
		return 0, err
	}
	return int(res.DeletedCount), nil
}

func (r *chainEventRepo) ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error) {
	return findMany[models.ChainEvent](ctx, r.coll,
		bson.M{"chainId": chainID, "address": address, "name": name},
		options.Find().SetSort(bson.D{{Key: "blockNumber", Value: 1}, {Key: "logIndex", Value: 1}}),
	)
}

func (r *chainEventRepo) ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error) {
	return findMany[models.ChainEvent](ctx, r.coll,
		bson.M{"chainId": chainID},
		options.Find().SetSort(bson.D{{Key: "blockNumber", Value: 1}, {Key: "logIndex", Value: 1}, {Key: "txHash", Value: 1}}),
	)
}
