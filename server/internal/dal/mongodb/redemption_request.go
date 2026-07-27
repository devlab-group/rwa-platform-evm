package mongodb

import (
	"context"
	"regexp"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type redemptionRequestRepo struct{ coll *mongo.Collection }

func (r *redemptionRequestRepo) Upsert(ctx context.Context, req *models.RedemptionRequest) error {
	return upsertByID(ctx, r.coll, req.ID, req)
}
func (r *redemptionRequestRepo) Get(ctx context.Context, id string) (*models.RedemptionRequest, error) {
	return getByID[models.RedemptionRequest](ctx, r.coll, id)
}
func (r *redemptionRequestRepo) List(ctx context.Context, status string) ([]*models.RedemptionRequest, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	return findMany[models.RedemptionRequest](ctx, r.coll, filter)
}
func (r *redemptionRequestRepo) DeleteAll(ctx context.Context) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{})
	return err
}

// DeleteStaleGeneration implements the generation-swap rebuild
// cleanup — see the interface doc comment.
func (r *redemptionRequestRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"generation": bson.M{"$ne": gen}})
	return err
}

// ListPage returns one bounded, repository-level keyset page — see
// PurchaseRepository.ListPage's doc comment. Sorted (createdAt desc, _id
// desc); optional status and beneficiary-address filters combine with the
// keyset $or via $and. The address filter matches the beneficiary
// case-insensitively (anchored $regex, "i"), mirroring
// TransactionRepository.ListPage's address query.
func (r *redemptionRequestRepo) ListPage(ctx context.Context, status, address, cursor string, limit int) ([]*models.RedemptionRequest, string, error) {
	clauses := bson.A{keysetFilter(cursor, "createdAt")}
	if status != "" {
		clauses = append(clauses, bson.M{"status": status})
	}
	if address != "" {
		pattern := "^" + regexp.QuoteMeta(address) + "$"
		clauses = append(clauses, bson.M{"beneficiary": bson.M{"$regex": pattern, "$options": "i"}})
	}
	items, err := findMany[models.RedemptionRequest](ctx, r.coll, bson.M{"$and": clauses},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(rr *models.RedemptionRequest) (int64, string) { return rr.CreatedAt, rr.ID })
}
