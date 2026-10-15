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

// adminChallengeRepo stores one address-keyed challenge in admin_challenges. Get
// enforces ExpiresAt independently of TTL cleanup, as walletSessionRepo.Get does.
type adminChallengeRepo struct{ coll *mongo.Collection }

func (r *adminChallengeRepo) Upsert(ctx context.Context, c *models.AdminChallenge) error {
	// Replace the address's challenge because verification supplies no nonce to select
	// among multiple messages
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
	// Match unused challenges atomically so only one concurrent verification consumes the
	// stored challenge
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": address, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// On no match, Get distinguishes absent/expired challenges from already-used ones
	if _, err := r.Get(ctx, address); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}
