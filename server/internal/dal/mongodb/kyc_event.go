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
	// claims is the "kyc_claims" collection ClaimLatestForAddress CASes
	// into, one document per address keyed by _id=address. Kept separate
	// from coll (kyc_events, the append-only
	// history) so the claim — a single mutable pointer per address — never
	// needs a query/sort over the full event history to determine.
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

func (r *kycEventRepo) ListPending(ctx context.Context) ([]*models.KYCEvent, error) {
	// Claiming is included so the reconciler picks up an event whose claim
	// was never resolved because Process died at the claim/finalize boundary.
	filter := bson.M{"applyStatus": bson.M{"$in": bson.A{models.KYCApplyClaiming, models.KYCApplyAccepted, models.KYCApplyApplying}}}
	return findMany[models.KYCEvent](ctx, r.coll, filter, options.Find().SetSort(bson.D{{Key: "occurredAt", Value: 1}}))
}

// kycClaim is the kyc_claims document shape: one per address, holding
// whichever (occurredAt,eventKey) currently "wins" for that subject.
type kycClaim struct {
	Address    string    `bson:"_id"`
	OccurredAt time.Time `bson:"occurredAt"`
	EventKey   string    `bson:"eventKey"`
}

// ClaimLatestForAddress is the Mongo half of the atomic CAS documented on
// the KYCEventRepository interface. The filter matches (and so only
// updates) a claims document that either doesn't exist yet — handled by
// upsert — or whose stored (occurredAt,eventKey) is strictly less than the
// caller's, using the SAME ordering claimIsNewer encodes on the memory
// side: later occurredAt wins outright; on an exact tie, the
// lexicographically greater eventKey wins. A duplicate-key error means a
// concurrent claim for a brand-new address inserted first — that
// concurrent claim is therefore >= ours under this ordering, so we lost.
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
