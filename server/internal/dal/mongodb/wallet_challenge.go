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
	// The filter's "used": bson.M{"$ne": true} makes this a genuine
	// compare-and-swap: it only matches (and so only applies the $set) a
	// document that is not already used, so a concurrent second MarkUsed
	// for the same id cannot also succeed — see the interface doc comment.
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// MatchedCount 0 means either "no such challenge" or "already used";
	// telling those apart needs one more read (this only happens on the
	// error path, never on the common success path above).
	if _, err := r.Get(ctx, id); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}

// CountActive backs ChallengeService.Create's per-address active-challenge
// cap. Uses CountDocuments (not an approximate
// estimate) since this gates a security-relevant decision, not a UI
// display figure; the address+used+expiresAt compound index declared in
// EnsureIndexes (internal/dal/mongodb/mongodb.go) backs this exact
// query shape.
func (r *walletChallengeRepo) CountActive(ctx context.Context, address string, now time.Time) (int, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"address": address, "used": false, "expiresAt": bson.M{"$gt": now}})
	return int(n), err
}
