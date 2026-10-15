package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// walletSessionRepo shares sessions across server replicas using MongoDB. Get enforces
// expiry before TTL cleanup. auth.SessionManager supplies SHA-256 token IDs, so
// collection dumps contain no raw bearer credentials.
type walletSessionRepo struct{ coll *mongo.Collection }

func (r *walletSessionRepo) Create(ctx context.Context, s *models.WalletSession) error {
	return createDocWithID(ctx, r.coll, s.Token, s)
}

func (r *walletSessionRepo) Get(ctx context.Context, token string) (*models.WalletSession, error) {
	s, err := getByID[models.WalletSession](ctx, r.coll, token)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		return nil, repository.ErrNotFound
	}
	return s, nil
}

func (r *walletSessionRepo) Delete(ctx context.Context, token string) error {
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": token})
	return err
}
