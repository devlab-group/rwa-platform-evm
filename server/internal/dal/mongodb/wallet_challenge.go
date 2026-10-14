package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type walletChallengeRepo struct{ coll *mongo.Collection }

func (r *walletChallengeRepo) Create(ctx context.Context, c *models.WalletChallenge) error {
	return createDoc(ctx, r.coll, c)
}

func (r *walletChallengeRepo) Get(ctx context.Context, id string) (*models.WalletChallenge, error) {
	return getByID[models.WalletChallenge](ctx, r.coll, id)
}

func (r *walletChallengeRepo) MarkUsed(ctx context.Context, id string) error {
	// the filter matches only a challenge that is not yet used, so the update is a
	// compare-and-set and a concurrent second MarkUsed for the id cannot also
	// succeed
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// MatchedCount 0 means no such challenge or already used. One more read tells
	// them apart, and only on this error path.
	if _, err := r.Get(ctx, id); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}

// CountActive backs the per-address cap in ChallengeService.Create. It uses
// CountDocuments and not an estimate because it gates a security decision. The
// address, used and expiresAt index from EnsureIndexes serves this query.
func (r *walletChallengeRepo) CountActive(ctx context.Context, address string, now time.Time) (int, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"address": address, "used": false, "expiresAt": bson.M{"$gt": now}})
	return int(n), err
}
