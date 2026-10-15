package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type assetProfileRepo struct{ coll *mongo.Collection }

func (r *assetProfileRepo) Get(ctx context.Context, projectID string) (*models.AssetProfile, error) {
	return getByID[models.AssetProfile](ctx, r.coll, projectID)
}

// GetCurrent returns the profile with the latest createdAt, or ErrNotFound when
// there is none
func (r *assetProfileRepo) GetCurrent(ctx context.Context) (*models.AssetProfile, error) {
	return findOne[models.AssetProfile](ctx, r.coll, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
}

func (r *assetProfileRepo) Upsert(ctx context.Context, p *models.AssetProfile) error {
	return upsertByID(ctx, r.coll, p.ProjectID, p)
}

// Create inserts without upsert; ProjectID's unique _id rejects concurrent duplicate profiles
func (r *assetProfileRepo) Create(ctx context.Context, p *models.AssetProfile) error {
	return createDoc(ctx, r.coll, p)
}
