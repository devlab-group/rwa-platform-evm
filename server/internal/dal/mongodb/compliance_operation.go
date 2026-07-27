package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type complianceOperationRepo struct{ coll *mongo.Collection }

func (r *complianceOperationRepo) Create(ctx context.Context, op *models.ComplianceOperation) error {
	return createDoc(ctx, r.coll, op)
}

func (r *complianceOperationRepo) List(ctx context.Context) ([]*models.ComplianceOperation, error) {
	return findMany[models.ComplianceOperation](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
}
