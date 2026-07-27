package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// walletSessionRepo persists wallet_sessions in a shared session store, so a
// token minted by one server replica validates, and a logout on any replica
// revokes it, on every replica (see
// repository.WalletSessionRepository's doc comment). A TTL index on
// expiresAt (EnsureIndexes) is a background cleanup convenience, not the
// correctness mechanism: Get always checks ExpiresAt itself first, so a
// token past expiry stops validating immediately rather than up to ~60s
// later when Mongo's TTL monitor gets around to deleting it.
//
// The `_id` stored here is the SHA-256 digest of the bearer
// token (auth.SessionManager derives it on both issue and lookup), never the
// token itself — a dump of this collection contains no usable credentials.
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
