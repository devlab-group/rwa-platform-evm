package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// deadLetterRepo persists indexer_dead_letters. See repository.DeadLetterRepository
// for Record's upsert and increment behavior. Record is one atomic upsert.
type deadLetterRepo struct{ coll *mongo.Collection }

func (r *deadLetterRepo) Record(ctx context.Context, e *models.DeadLetterEntry) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": e.ID}, deadLetterRecordUpdate(e), options.UpdateOne().SetUpsert(true))
	return err
}

// deadLetterRecordUpdate builds Record's update as an aggregation pipeline, so one
// upsert can branch on whether the entry exists. A new entry is stored as given. A
// repeat keeps firstFailedAt, bumps retryCount, un-resolves the entry and takes the
// latest failure fields. $literal keeps values that start with "$" from being read
// as field paths.
func deadLetterRecordUpdate(e *models.DeadLetterEntry) mongo.Pipeline {
	lit := func(v any) bson.M { return bson.M{"$literal": v} }
	return mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"chainId": lit(e.ChainID), "blockNumber": lit(e.BlockNumber), "blockHash": lit(e.BlockHash),
		"txHash": lit(e.TxHash), "logIndex": lit(e.LogIndex), "eventSignature": lit(e.EventSignature),
		"errorCategory": lit(e.ErrorCategory), "errorMessage": lit(e.ErrorMessage),
		"lastFailedAt": lit(e.LastFailedAt), "sourceData": lit(e.SourceData),
		"firstFailedAt": bson.M{"$ifNull": bson.A{"$firstFailedAt", lit(e.FirstFailedAt)}},
		"resolved": bson.M{"$cond": bson.A{
			bson.M{"$eq": bson.A{bson.M{"$type": "$retryCount"}, "missing"}}, lit(e.Resolved), false}},
		"retryCount": bson.M{"$ifNull": bson.A{bson.M{"$add": bson.A{"$retryCount", 1}}, lit(e.RetryCount)}},
	}}}}
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
