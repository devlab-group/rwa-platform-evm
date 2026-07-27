package mongodb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// newFencingToken generates a random per-reservation fencing/lease token —
// see internal/dal/memory's identical helper doc comment; duplicated rather
// than shared across the two
// implementation packages to keep each storage backend self-contained.
func newFencingToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type idempotencyRepo struct{ coll *mongo.Collection }

func (r *idempotencyRepo) Get(ctx context.Context, key string) (*models.IdempotencyRecord, error) {
	return getByID[models.IdempotencyRecord](ctx, r.coll, key)
}

// Reserve claims key via an atomic upsert filtered on "no live (non-expired)
// document currently holds this _id": the filter {_id: key, expiresAt:
// {$lt: now}} matches either
// no document at all (first-ever Reserve for key) or an EXPIRED one (the
// original owner crashed/was too slow); FindOneAndReplace with Upsert then
// atomically installs a brand-new record — fresh token, fresh CreatedAt/
// ExpiresAt — as the ONE operation MongoDB performs for a matching filter.
// If a LIVE (non-expired) document already holds _id=key, the filter does
// not match it, so the upsert instead attempts an insert that collides with
// that existing _id and fails with a duplicate-key error — exactly the
// existing-reservation-or-completed-response case, which is resolved by a
// follow-up Get. This is the same "the document's filter membership IS the
// atomicity guarantee" pattern the old InsertOne-only version used, widened
// to also cover expired takeover atomically (a second, concurrent takeover
// attempt racing in immediately after one wins gets the same duplicate-key
// outcome, never two winners).
func (r *idempotencyRepo) Reserve(ctx context.Context, key, method, path, requestHash string, ttl time.Duration) (*models.IdempotencyRecord, bool, string, error) {
	now := time.Now().UTC()
	token, err := newFencingToken()
	if err != nil {
		return nil, false, "", err
	}
	rec := &models.IdempotencyRecord{
		Key: key, Method: method, Path: path, RequestHash: requestHash, Token: token,
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	filter := bson.M{"_id": key, "expiresAt": bson.M{"$lt": now}}
	opts := options.FindOneAndReplace().SetUpsert(true)
	err = r.coll.FindOneAndReplace(ctx, filter, rec, opts).Err()
	if err == nil || errors.Is(err, mongo.ErrNoDocuments) {
		// ErrNoDocuments here means "nothing matched the filter to return
		// as the PRE-replace document" (the common case: key didn't exist
		// yet), NOT a failure — the upsert still inserted rec.
		return nil, true, token, nil
	}
	if mongo.IsDuplicateKeyError(err) {
		existing, getErr := r.Get(ctx, key)
		if getErr != nil {
			return nil, false, "", getErr
		}
		existing.Token = "" // never hand a live reservation's fencing token to a caller that didn't win it
		return existing, false, "", nil
	}
	return nil, false, "", err
}

// Complete fills in rec's response fields, matched by _id AND token
// together (fencing): if no document currently holds
// both key and token — either Reserve was never called for key, or this
// caller's reservation already expired and was taken over by a later
// Reserve — MatchedCount is 0 and this returns ErrFencingTokenMismatch
// instead of silently doing nothing (the pre-fencing behavior) or, worse,
// overwriting a different owner's record.
func (r *idempotencyRepo) Complete(ctx context.Context, key, token string, status int, body []byte) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": key, "token": token}, bson.M{"$set": bson.M{"responseStatus": status, "responseBody": body}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrFencingTokenMismatch
	}
	return nil
}

// Release deletes a reservation that will never be Complete()d (the
// handler's response wasn't cacheable) — see the interface doc comment for
// why this must not just leave the reservation sitting at ResponseStatus 0.
// Matched by _id AND token together, same fencing reasoning as Complete.
func (r *idempotencyRepo) Release(ctx context.Context, key, token string) error {
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": key, "token": token})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return repository.ErrFencingTokenMismatch
	}
	return nil
}
