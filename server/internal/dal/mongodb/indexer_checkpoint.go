package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type indexerCheckpointRepo struct{ coll *mongo.Collection }

func (r *indexerCheckpointRepo) Get(ctx context.Context, chainID int64, address string) (*models.IndexerCheckpoint, error) {
	return findOne[models.IndexerCheckpoint](ctx, r.coll, bson.M{"chainId": chainID, "address": address})
}

func (r *indexerCheckpointRepo) Set(ctx context.Context, c *models.IndexerCheckpoint) error {
	_, err := r.coll.ReplaceOne(ctx, bson.M{"chainId": c.ChainID, "address": c.Address}, c, options.Replace().SetUpsert(true))
	return err
}
