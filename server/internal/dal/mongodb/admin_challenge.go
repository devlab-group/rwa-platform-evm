package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// adminChallengeRepo stores nonce-keyed admin_challenges; Get enforces expiry without waiting for
// TTL cleanup
type adminChallengeRepo struct{ coll *mongo.Collection }

func (r *adminChallengeRepo) Insert(ctx context.Context, c *models.AdminChallenge) error {
	_, err := r.coll.InsertOne(ctx, c)
	return err
}

func (r *adminChallengeRepo) Get(ctx context.Context, nonce string) (*models.AdminChallenge, error) {
	c, err := getByID[models.AdminChallenge](ctx, r.coll, nonce)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(c.ExpiresAt) {
		return nil, repository.ErrNotFound
	}
	return c, nil
}

func (r *adminChallengeRepo) MarkUsed(ctx context.Context, nonce string) error {
	// Match unused challenges atomically so only one concurrent verification consumes the
	// stored challenge
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": nonce, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// On no match, Get distinguishes absent/expired challenges from already-used ones
	if _, err := r.Get(ctx, nonce); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}
