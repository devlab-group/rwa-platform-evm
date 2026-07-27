package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// deadLetterRepo persists indexer_dead_letters (see
// repository.DeadLetterRepository's doc comment on Record's
// upsert-with-increment semantics).
type deadLetterRepo struct{ coll *mongo.Collection }

func (r *deadLetterRepo) Record(ctx context.Context, e *models.DeadLetterEntry) error {
	existing, err := findOne[models.DeadLetterEntry](ctx, r.coll, bson.M{"_id": e.ID})
	if err != nil && err != repository.ErrNotFound {
		return err
	}
	cp := *e
	if existing != nil {
		cp.FirstFailedAt = existing.FirstFailedAt
		cp.RetryCount = existing.RetryCount + 1
		cp.Resolved = false
	}
	_, err = r.coll.ReplaceOne(ctx, bson.M{"_id": e.ID}, cp, options.Replace().SetUpsert(true))
	return err
}

func (r *deadLetterRepo) Get(ctx context.Context, id string) (*models.DeadLetterEntry, error) {
	return getByID[models.DeadLetterEntry](ctx, r.coll, id)
}

func (r *deadLetterRepo) List(ctx context.Context) ([]*models.DeadLetterEntry, error) {
	return findMany[models.DeadLetterEntry](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "firstFailedAt", Value: 1}}))
}

func (r *deadLetterRepo) Resolve(ctx context.Context, id string) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"resolved": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}
