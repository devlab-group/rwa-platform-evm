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

// nonceLeaseRepo persists nonce_leases (distributed multi-replica nonce
// coordination — see models.NonceLease's doc comment).
//
// Deliberately no TTL index on expiresAt: EnsureIndexes does not register
// one for this collection, and Acquire/Renew/Release below never rely on
// (or benefit from) Mongo auto-deleting an expired document — an
// auto-delete would let a subsequent Acquire's $ifNull-based token
// increment reset back to 1, breaking the strict-monotonicity guarantee
// the whole fencing scheme depends on (see models.NonceLease). The
// collection stays tiny regardless (one document per configured hot key).
type nonceLeaseRepo struct{ coll *mongo.Collection }

// Acquire uses the same "the filter's match/no-match IS the atomicity"
// pattern as idempotencyRepo.Reserve (see its doc comment): the filter
// matches either no document at all (first-ever Acquire for key) or an
// EXPIRED one, and an aggregation-pipeline update lets the new token be
// computed FROM the (possibly absent) existing one in the same atomic
// operation ($ifNull($token, 0) + 1) — a plain $set update could not
// express "increment relative to whatever's already there, defaulting to
// 0" without a separate read. A LIVE (non-expired) document under _id=key
// does not match the filter, so the upsert instead attempts an insert that
// collides with that existing _id and fails with a duplicate-key error —
// resolved below as simply "not acquired," exactly mirroring Reserve's
// handling of the same race.
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

// Release marks the document immediately expired (via $set, not $delete) —
// see memory.NonceLeaseRepository.Release's doc comment for why deleting
// it would reset Acquire's token counter and reintroduce the exact
// ABA-style fencing-token collision this whole mechanism exists to
// prevent.
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
