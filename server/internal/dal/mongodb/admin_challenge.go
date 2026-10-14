package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// adminChallengeRepo persists admin_challenges, one active challenge per address
// with the normalized address as _id (see models.AdminChallenge). The TTL index on
// expiresAt is background cleanup. Get checks ExpiresAt itself, as walletSessionRepo.Get
// does.
type adminChallengeRepo struct{ coll *mongo.Collection }

func (r *adminChallengeRepo) Upsert(ctx context.Context, c *models.AdminChallenge) error {
	// replace any earlier document for this _id, used or not, because the verify step
	// has no client-supplied nonce to choose between several
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": c.Address}, c, options.Replace().SetUpsert(true))
	return err
}

func (r *adminChallengeRepo) Get(ctx context.Context, address string) (*models.AdminChallenge, error) {
	c, err := getByID[models.AdminChallenge](ctx, r.coll, address)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(c.ExpiresAt) {
		return nil, repository.ErrNotFound
	}
	return c, nil
}

func (r *adminChallengeRepo) MarkUsed(ctx context.Context, address string) error {
	// the filter matches only a challenge that is not yet used, so two concurrent
	// verifies of the same signature cannot both succeed, as in
	// walletChallengeRepo.MarkUsed
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": address, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// MatchedCount 0 means no such challenge or one already used. A read tells them
	// apart, and Get reports an expired challenge as not found.
	if _, err := r.Get(ctx, address); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}
