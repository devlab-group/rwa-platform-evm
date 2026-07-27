package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type purchaseRepo struct{ coll *mongo.Collection }

func (r *purchaseRepo) Create(ctx context.Context, p *models.Purchase) error {
	return createDoc(ctx, r.coll, p)
}
func (r *purchaseRepo) List(ctx context.Context) ([]*models.Purchase, error) {
	return findMany[models.Purchase](ctx, r.coll, bson.M{})
}
func (r *purchaseRepo) DeleteAll(ctx context.Context) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{})
	return err
}

// Upsert and DeleteStaleGeneration implement the generation-swap
// rebuild — see the interface doc comments.
func (r *purchaseRepo) Upsert(ctx context.Context, p *models.Purchase) error {
	return upsertByID(ctx, r.coll, p.ID, p)
}

func (r *purchaseRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"generation": bson.M{"$ne": gen}})
	return err
}

// ListPage returns one bounded, repository-level keyset page — see
// the interface doc comment. Sorted (blockNumber desc, _id desc); the
// filter/limit are pushed into the query itself (keysetFilter +
// SetLimit(limit+1)) rather than fetching the collection and slicing in
// the caller.
func (r *purchaseRepo) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Purchase, string, error) {
	items, err := findMany[models.Purchase](ctx, r.coll, keysetFilter(cursor, "blockNumber"),
		options.Find().SetSort(bson.D{{Key: "blockNumber", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(p *models.Purchase) (int64, string) { return int64(p.BlockNumber), p.ID })
}
