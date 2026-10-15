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

// nonceLeaseRepo persists models.NonceLease without TTL cleanup or deletion, preserving
// increasing fencing tokens with one document per hot key
type nonceLeaseRepo struct{ coll *mongo.Collection }

// Acquire conditionally upserts absent or expired leases by _id. Its pipeline computes
// $ifNull($token, 0)+1 atomically without a separate read. A live lease causes an _id
// collision, reported as not acquired, matching idempotencyRepo.Reserve's contention
// pattern.
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

// Release expires the lease without deleting it, preserving increasing tokens and
// rejecting stale holder/token pairs
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
