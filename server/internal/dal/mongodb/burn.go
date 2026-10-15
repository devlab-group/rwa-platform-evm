package mongodb

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type burnRepo struct{ coll *mongo.Collection }

func (r *burnRepo) Get(ctx context.Context, burnID string) (*models.Burn, error) {
	return getByID[models.Burn](ctx, r.coll, burnID)
}

// GetByOperationID lowercases the lookup key to match stored operation IDs
func (r *burnRepo) GetByOperationID(ctx context.Context, operationID string) (*models.Burn, error) {
	var out models.Burn
	err := r.coll.FindOne(ctx, bson.M{"operationId": strings.ToLower(operationID)}).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *burnRepo) ListByRecord(ctx context.Context, recordID string) ([]*models.Burn, error) {
	return findMany[models.Burn](ctx, r.coll, bson.M{"recordId": recordID})
}

func (r *burnRepo) Create(ctx context.Context, b *models.Burn) error {
	return createDoc(ctx, r.coll, b)
}

// UpdateConditional atomically checks _id and version, then increments Version.
// Get distinguishes a missing burn from a version conflict.
func (r *burnRepo) UpdateConditional(ctx context.Context, b *models.Burn, expectedVersion int) (bool, error) {
	cp := *b
	cp.Version = expectedVersion + 1
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": b.BurnID, "version": expectedVersion}, cp)
	if err != nil {
		return false, err
	}
	if res.MatchedCount > 0 {
		return true, nil
	}
	if _, err := r.Get(ctx, b.BurnID); err != nil {
		return false, err
	}
	return false, nil
}

// ListPage returns one keyset page sorted by (createdAt, _id) descending
func (r *burnRepo) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Burn, string, error) {
	items, err := findMany[models.Burn](ctx, r.coll, keysetFilterDirDate(cursor, "createdAt", true),
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(b *models.Burn) (int64, string) { return b.CreatedAt.UnixNano(), b.BurnID })
}
