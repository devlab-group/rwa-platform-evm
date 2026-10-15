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

// newFencingToken creates a random 128-bit reservation token independently of the
// memory backend
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

// Reserve uses FindOneAndReplace with upsert on _id and expiresAt < now. New or expired
// records get fresh tokens and timestamps. A live row or lost takeover collides on _id;
// return the existing reservation/response with its token cleared.
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
		// ErrNoDocuments means upsert inserted rec without an earlier document to return
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

// Complete matches key and token, returning ErrFencingTokenMismatch when absent or
// superseded so an old owner cannot overwrite a later reservation
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

// Release deletes a non-cacheable reservation only when key and token match; see
// IdempotencyRepository.Release
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
