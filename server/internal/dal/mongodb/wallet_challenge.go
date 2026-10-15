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
	// Match Used=false atomically so a concurrent second MarkUsed cannot consume the same
	// challenge
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// Read only after a failed match to distinguish a missing challenge from an
	// already-used one
	if _, err := r.Get(ctx, id); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}

// CountActive uses indexed CountDocuments for ChallengeService.Create's active-count
// check, avoiding an estimate; EnsureIndexes supplies (address, used, expiresAt)
func (r *walletChallengeRepo) CountActive(ctx context.Context, address string, now time.Time) (int, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"address": address, "used": false, "expiresAt": bson.M{"$gt": now}})
	return int(n), err
}
