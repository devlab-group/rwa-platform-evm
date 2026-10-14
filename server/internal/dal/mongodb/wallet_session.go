package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// walletSessionRepo persists wallet_sessions in a store shared by server replicas,
// so a token issued by one replica validates on every replica and a logout on any
// replica revokes it everywhere (see repository.WalletSessionRepository). The TTL
// index on expiresAt is background cleanup. Get checks ExpiresAt itself, so an
// expired token stops validating at once and not up to about 60 seconds later when
// the MongoDB TTL monitor deletes it.
//
// The stored _id is the SHA-256 digest of the bearer token, which
// auth.SessionManager derives on issue and lookup, so a dump of the collection
// contains no usable credentials.
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
