package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
)

type attestationRepo struct{ coll *mongo.Collection }

func (r *attestationRepo) Get(ctx context.Context, recordID string) (*models.Attestation, error) {
	return getByID[models.Attestation](ctx, r.coll, recordID)
}

func (r *attestationRepo) Upsert(ctx context.Context, a *models.Attestation) error {
	return upsertByID(ctx, r.coll, a.RecordID, a)
}
