package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
)

type kycVerificationRepo struct{ coll *mongo.Collection }

func (r *kycVerificationRepo) Upsert(ctx context.Context, v *models.KYCVerification) error {
	v.ID = models.KYCVerificationID(v.Provider, v.Ref)
	return upsertByID(ctx, r.coll, v.ID, v)
}

func (r *kycVerificationRepo) GetByRef(ctx context.Context, provider, ref string) (*models.KYCVerification, error) {
	return getByID[models.KYCVerification](ctx, r.coll, models.KYCVerificationID(provider, ref))
}
