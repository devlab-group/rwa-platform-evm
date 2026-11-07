package mongodb

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type purchaseOrderRepo struct{ coll *mongo.Collection }

// purchaseOrderDoc stores an order with BuyerKey, its buyer in lowercase. Reads decode into
// models.PurchaseOrder alone, which ignores the extra field.
type purchaseOrderDoc struct {
	models.PurchaseOrder `bson:",inline"`
	BuyerKey             string `bson:"buyerKey"`
}

func (r *purchaseOrderRepo) Upsert(ctx context.Context, o *models.PurchaseOrder) error {
	return upsertByID(ctx, r.coll, o.ID, purchaseOrderDoc{PurchaseOrder: *o, BuyerKey: strings.ToLower(o.Buyer)})
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

// purchaseOrderSort is ListPage's order, newest first with ties broken by id
var purchaseOrderSort = bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}

// purchaseOrderFilter combines the keyset, status and buyer filters with $and. The buyer
// matches the lowercase buyerKey exactly, so the (buyerKey, createdAt, _id) index serves it
// and an unauthenticated caller cannot force collection scans.
func purchaseOrderFilter(status, address, cursor string) bson.M {
	clauses := bson.A{keysetFilter(cursor, "createdAt")}
	if status != "" {
		clauses = append(clauses, bson.M{"status": status})
	}
	if address != "" {
		clauses = append(clauses, bson.M{"buyerKey": strings.ToLower(address)})
	}
	return bson.M{"$and": clauses}
}

// ListPage returns one page in purchaseOrderSort order, as redemptionRequestRepo.ListPage does
func (r *purchaseOrderRepo) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.PurchaseOrder, string, error) {
	items, err := findMany[models.PurchaseOrder](ctx, r.coll, purchaseOrderFilter(status, address, cursor),
		options.Find().SetSort(purchaseOrderSort).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(o *models.PurchaseOrder) (int64, string) { return o.CreatedAt, o.ID })
}
