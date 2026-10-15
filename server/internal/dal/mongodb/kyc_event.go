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

type kycEventRepo struct {
	coll *mongo.Collection
	// claims stores one mutable (occurredAt, eventKey) per address in kyc_claims, keyed by
	// _id. Keeping it separate from delivery history avoids scanning or sorting history to
	// find the claim.
	claims *mongo.Collection
}

func (r *kycEventRepo) Exists(ctx context.Context, payloadHash string) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"payloadHash": payloadHash})
	return n > 0, err
}

func (r *kycEventRepo) Create(ctx context.Context, e *models.KYCEvent) error {
	return createDoc(ctx, r.coll, e)
}

func (r *kycEventRepo) Update(ctx context.Context, e *models.KYCEvent) error {
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": e.ID}, e)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *kycEventRepo) List(ctx context.Context) ([]*models.KYCEvent, error) {
	return findMany[models.KYCEvent](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "receivedAt", Value: 1}}))
}

// ListAppliedWithTx backs the deep-reorg reopen scan and uses the applyStatus index
func (r *kycEventRepo) ListAppliedWithTx(ctx context.Context) ([]*models.KYCEvent, error) {
	filter := bson.M{"applyStatus": models.KYCApplyApplied, "txId": bson.M{"$gt": ""}}
	return findMany[models.KYCEvent](ctx, r.coll, filter, options.Find().SetSort(bson.D{{Key: "occurredAt", Value: 1}}))
}

func (r *kycEventRepo) ListPending(ctx context.Context) ([]*models.KYCEvent, error) {
	// Include Claiming so the reconciler can finish interrupted claim finalization
	filter := bson.M{"applyStatus": bson.M{"$in": bson.A{models.KYCApplyClaiming, models.KYCApplyAccepted, models.KYCApplyApplying}}}
	return findMany[models.KYCEvent](ctx, r.coll, filter, options.Find().SetSort(bson.D{{Key: "occurredAt", Value: 1}}))
}

// kycClaim is the kyc_claims document: the (occurredAt, eventKey) that currently
// wins for one address
type kycClaim struct {
	Address    string    `bson:"_id"`
	OccurredAt time.Time `bson:"occurredAt"`
	EventKey   string    `bson:"eventKey"`
}

// ClaimLatestForAddress conditionally upserts by address, accepting a greater
// occurredAt or, on a tie, greater eventKey, as claimIsNewer does. Absent claims are
// inserted; duplicate-key conflicts return false rather than advancing the claim.
func (r *kycEventRepo) ClaimLatestForAddress(ctx context.Context, address string, occurredAt time.Time, eventKey string) (bool, error) {
	filter := bson.M{
		"_id": address,
		"$or": bson.A{
			bson.M{"occurredAt": bson.M{"$lt": occurredAt}},
			bson.M{"occurredAt": occurredAt, "eventKey": bson.M{"$lt": eventKey}},
		},
	}
	update := bson.M{"$set": bson.M{"occurredAt": occurredAt, "eventKey": eventKey}}
	res, err := r.claims.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, err
	}
	return res.MatchedCount > 0 || res.UpsertedCount > 0, nil
}

func (r *kycEventRepo) CurrentClaimEventKey(ctx context.Context, address string) (string, error) {
	claim, err := getByID[kycClaim](ctx, r.claims, address)
	if err != nil {
		return "", err
	}
	return claim.EventKey, nil
}
