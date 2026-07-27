package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

// publicationRepo persists ipfs_publications.
type publicationRepo struct{ coll *mongo.Collection }

func (r *publicationRepo) Get(ctx context.Context, id string) (*models.PublicationRecord, error) {
	return getByID[models.PublicationRecord](ctx, r.coll, id)
}

func (r *publicationRepo) Upsert(ctx context.Context, rec *models.PublicationRecord) error {
	return upsertByID(ctx, r.coll, rec.ID, rec)
}

func (r *publicationRepo) List(ctx context.Context) ([]*models.PublicationRecord, error) {
	return findMany[models.PublicationRecord](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
}
