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

// newFencingToken returns a random 128-bit token that identifies one reservation.
// It is duplicated in internal/dal/memory so each backend stays self-contained.
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

// Reserve atomically replaces an expired _id or inserts an absent one using
// FindOneAndReplace with upsert and a filter on _id and expiresAt < now.
// The new record gets a fresh fencing token, CreatedAt and ExpiresAt.
//
// A live record does not match the filter, so insertion collides with its unique
// _id; a losing concurrent takeover gets the same duplicate-key error.
// Get then returns the existing reservation or response without its token.
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
		// no document matched, so there was no earlier document to return, and the upsert
		// inserted rec; that is not a failure
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

// Complete stores the response on the document with this key and token. If none
// exists, because Reserve was never called for key or this caller's reservation
// expired and was taken over, MatchedCount is 0 and it returns
// ErrFencingTokenMismatch, so it never overwrites another owner's record.
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

// Release deletes a reservation that will not be completed, because the response
// was not cacheable (see the interface doc comment). It matches by key and token,
// as Complete does.
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
