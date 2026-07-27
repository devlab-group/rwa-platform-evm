package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
)

type auditLogRepo struct{ coll *mongo.Collection }

func (r *auditLogRepo) Append(ctx context.Context, e *models.AuditLogEntry) error {
	return createDoc(ctx, r.coll, e)
}

func (r *auditLogRepo) List(ctx context.Context, category string, limit int) ([]*models.AuditLogEntry, error) {
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	filter := bson.M{}
	if category != "" {
		filter["category"] = category
	}
	return findMany[models.AuditLogEntry](ctx, r.coll, filter, opts)
}
