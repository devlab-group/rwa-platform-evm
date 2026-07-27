package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
)

type auditPackageRepo struct{ coll *mongo.Collection }

func (r *auditPackageRepo) Get(ctx context.Context, recordID string) (*models.AuditPackage, error) {
	return getByID[models.AuditPackage](ctx, r.coll, recordID)
}

func (r *auditPackageRepo) Upsert(ctx context.Context, p *models.AuditPackage) error {
	return upsertByID(ctx, r.coll, p.RecordID, p)
}
