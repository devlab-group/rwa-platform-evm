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

// walletSessionRepo persists wallet_sessions in a shared session store, so a
// token minted by one server replica validates, and a logout on any replica
// revokes it, on every replica (see
// repository.WalletSessionRepository's doc comment). A TTL index on
// expiresAt (EnsureIndexes) is a background cleanup convenience, not the
// correctness mechanism: Get always checks ExpiresAt itself first, so a
// token past expiry stops validating immediately rather than up to ~60s
// later when Mongo's TTL monitor gets around to deleting it.
//
// The `_id` stored here is the SHA-256 digest of the bearer
// token (auth.SessionManager derives it on both issue and lookup), never the
// token itself — a dump of this collection contains no usable credentials.
type walletSessionRepo struct{ coll *mongo.Collection }

func (r *walletSessionRepo) Create(ctx context.Context, s *models.WalletSession) error {
	return createDocWithID(ctx, r.coll, s.Token, s)
}

func (r *walletSessionRepo) Get(ctx context.Context, token string) (*models.WalletSession, error) {
	s, err := getByID[models.WalletSession](ctx, r.coll, token)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		return nil, repository.ErrNotFound
	}
	return s, nil
}

func (r *walletSessionRepo) Delete(ctx context.Context, token string) error {
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": token})
	return err
}

// adminChallengeRepo persists admin_challenges (one active challenge per
// address, keyed by the normalized address as _id — see
// models.AdminChallenge and repository.AdminChallengeRepository). A TTL
// index on expiresAt (EnsureIndexes) is background cleanup only; Get checks
// ExpiresAt itself, same reasoning as walletSessionRepo.Get.
type adminChallengeRepo struct{ coll *mongo.Collection }

func (r *adminChallengeRepo) Upsert(ctx context.Context, c *models.AdminChallenge) error {
	// One active challenge per address: replace any prior document for this
	// _id (used or not) rather than insert, since the verify step has no
	// client-echoed nonce to pick among several.
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": c.Address}, c, options.Replace().SetUpsert(true))
	return err
}

func (r *adminChallengeRepo) Get(ctx context.Context, address string) (*models.AdminChallenge, error) {
	c, err := getByID[models.AdminChallenge](ctx, r.coll, address)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(c.ExpiresAt) {
		return nil, repository.ErrNotFound
	}
	return c, nil
}

func (r *adminChallengeRepo) MarkUsed(ctx context.Context, address string) error {
	// "used": {$ne: true} makes this a genuine compare-and-swap so two
	// concurrent verifies of the same signature cannot both succeed — same
	// pattern as walletChallengeRepo.MarkUsed.
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": address, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// MatchedCount 0 means either "no such challenge" or "already used" (or
	// "expired", which Get treats as not found) — one extra read on the
	// error path tells them apart.
	if _, err := r.Get(ctx, address); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}
