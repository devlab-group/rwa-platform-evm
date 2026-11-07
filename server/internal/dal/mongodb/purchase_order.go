package mongodb

import (
	"context"
	"regexp"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type purchaseOrderRepo struct{ coll *mongo.Collection }

func (r *purchaseOrderRepo) Upsert(ctx context.Context, o *models.PurchaseOrder) error {
	return upsertByID(ctx, r.coll, o.ID, o)
}
func (r *purchaseOrderRepo) Get(ctx context.Context, id string) (*models.PurchaseOrder, error) {
	return getByID[models.PurchaseOrder](ctx, r.coll, id)
}
func (r *purchaseOrderRepo) DeleteAll(ctx context.Context) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{})
	return err
}

// DeleteStaleGeneration implements the cleanup step of the generation-swap rebuild,
// see the interface doc comment
func (r *purchaseOrderRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"generation": bson.M{"$ne": gen}})
	return err
}

// ListPage sorts (createdAt, _id) descending, combining keyset, status and
// case-insensitive buyer filters with $and, as redemptionRequestRepo.ListPage does
func (r *purchaseOrderRepo) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.PurchaseOrder, string, error) {
	clauses := bson.A{keysetFilter(cursor, "createdAt")}
	if status != "" {
		clauses = append(clauses, bson.M{"status": status})
	}
	if address != "" {
		pattern := "^" + regexp.QuoteMeta(address) + "$"
		clauses = append(clauses, bson.M{"buyer": bson.M{"$regex": pattern, "$options": "i"}})
	}
	items, err := findMany[models.PurchaseOrder](ctx, r.coll, bson.M{"$and": clauses},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(o *models.PurchaseOrder) (int64, string) { return o.CreatedAt, o.ID })
}
