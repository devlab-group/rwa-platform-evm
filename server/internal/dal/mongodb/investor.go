package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type investorRepo struct{ coll *mongo.Collection }

func (r *investorRepo) Get(ctx context.Context, address string) (*models.Investor, error) {
	return getByID[models.Investor](ctx, r.coll, address)
}

func (r *investorRepo) List(ctx context.Context) ([]*models.Investor, error) {
	return findMany[models.Investor](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
}

// ListPage returns one bounded, ascending-address page — see the
// interface doc comment. Address is already _id, so the cursor is simply
// a "$gt" on _id itself; no compound KeysetCursor is needed the way
// purchases/redemption requests need one.
func (r *investorRepo) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Investor, string, error) {
	filter := bson.M{}
	if cursor != "" {
		filter = bson.M{"_id": bson.M{"$gt": cursor}}
	}
	items, err := findMany[models.Investor](ctx, r.coll, filter,
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	if len(items) <= limit {
		return items, "", nil
	}
	page := items[:limit]
	return page, page[limit-1].Address, nil
}

func (r *investorRepo) Upsert(ctx context.Context, inv *models.Investor) error {
	return upsertByID(ctx, r.coll, inv.Address, inv)
}
