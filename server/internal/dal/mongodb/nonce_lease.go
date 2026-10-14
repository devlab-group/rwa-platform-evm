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

// nonceLeaseRepo persists nonce_leases (see models.NonceLease).
//
// The collection has no TTL index, and no code deletes expired documents. Deleting
// one would reset the token that Acquire computes from the stored one, and the
// token must only increase. The collection stays small, with one document per hot
// key.
type nonceLeaseRepo struct{ coll *mongo.Collection }

// Acquire uses the same atomic filter as idempotencyRepo.Reserve. The filter
// matches no document, for a new key, or an expired one. An aggregation-pipeline
// update computes the new token from the stored one in the same operation
// ($ifNull($token, 0) + 1), which a plain $set cannot do without a separate read.
// A live document under _id=key does not match, so the upsert tries to insert, the
// _id collides and it fails with a duplicate key error, which is reported as not
// acquired.
func (r *nonceLeaseRepo) Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (uint64, bool, error) {
	now := time.Now().UTC()
	filter := bson.M{"_id": key, "expiresAt": bson.M{"$lt": now}}
	pipeline := mongo.Pipeline{
		{{Key: "$set", Value: bson.M{
			"holderId":  holderID,
			"token":     bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$token", 0}}, 1}},
			"expiresAt": now.Add(ttl),
			"updatedAt": now,
		}}},
	}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var out models.NonceLease
	err := r.coll.FindOneAndUpdate(ctx, filter, pipeline, opts).Decode(&out)
	switch {
	case err == nil:
		return out.Token, true, nil
	case mongo.IsDuplicateKeyError(err):
		return 0, false, nil // held live by someone else
	default:
		return 0, false, err
	}
}

func (r *nonceLeaseRepo) Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": key, "holderId": holderID, "token": token},
		bson.M{"$set": bson.M{"expiresAt": now.Add(ttl), "updatedAt": now}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// Release sets the document's expiry to now and does not delete it. A deleted
// document would restart Acquire's token counter at 1, and a delayed write from an
// earlier session of the same holder could then collide with a later session that
// reused a token.
func (r *nonceLeaseRepo) Release(ctx context.Context, key, holderID string, token uint64) error {
	now := time.Now().UTC()
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": key, "holderId": holderID, "token": token},
		bson.M{"$set": bson.M{"expiresAt": now, "updatedAt": now}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrFencingTokenMismatch
	}
	return nil
}
