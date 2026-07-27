package mongodb

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type assetRecordRepo struct{ coll *mongo.Collection }

func (r *assetRecordRepo) List(ctx context.Context) ([]*models.AssetRecord, error) {
	return findMany[models.AssetRecord](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
}

// ListPage returns one bounded, repository-level keyset page — see
// the interface doc comment. Sorted (createdAt asc, _id asc), matching
// List's pre-existing order.
func (r *assetRecordRepo) ListPage(ctx context.Context, cursor string, limit int) ([]*models.AssetRecord, string, error) {
	items, err := findMany[models.AssetRecord](ctx, r.coll, keysetFilterDirDate(cursor, "createdAt", false),
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(r *models.AssetRecord) (int64, string) { return r.CreatedAt.UnixNano(), r.RecordID })
}

func (r *assetRecordRepo) Get(ctx context.Context, recordID string) (*models.AssetRecord, error) {
	return getByID[models.AssetRecord](ctx, r.coll, recordID)
}

func (r *assetRecordRepo) Create(ctx context.Context, rec *models.AssetRecord) error {
	return createDoc(ctx, r.coll, rec)
}

func (r *assetRecordRepo) Update(ctx context.Context, rec *models.AssetRecord) error {
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": rec.RecordID}, rec)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// UpdateConditional is the storage-level CAS (mirror of
// transactionRepo.UpdateConditional): the filter's version match/no-match IS
// the atomicity. MatchedCount 0 means either the id is gone or a concurrent
// writer advanced version, distinguished by a follow-up Get.
func (r *assetRecordRepo) UpdateConditional(ctx context.Context, rec *models.AssetRecord, expectedVersion int) (bool, error) {
	cp := *rec
	cp.Version = expectedVersion + 1
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": rec.RecordID, "version": expectedVersion}, cp)
	if err != nil {
		return false, err
	}
	if res.MatchedCount > 0 {
		return true, nil
	}
	if _, err := r.Get(ctx, rec.RecordID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, repository.ErrNotFound
		}
		return false, err
	}
	return false, nil
}
