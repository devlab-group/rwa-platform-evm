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

type investorRepo struct{ coll *mongo.Collection }

func (r *investorRepo) Get(ctx context.Context, address string) (*models.Investor, error) {
	return getByID[models.Investor](ctx, r.coll, address)
}

func (r *investorRepo) List(ctx context.Context) ([]*models.Investor, error) {
	return findMany[models.Investor](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
}

// ListPage returns one bounded, ascending-address page — see the
// interface doc comment. Address is already _id, so the cursor is simply
// a "$gt" on _id itself; no compound KeysetCursor is needed the way
// purchases/redemption requests need one.
func (r *investorRepo) ListPage(ctx context.Context, cursor string, limit int) ([]*models.Investor, string, error) {
	filter := bson.M{}
	if cursor != "" {
		filter = bson.M{"_id": bson.M{"$gt": cursor}}
	}
	items, err := findMany[models.Investor](ctx, r.coll, filter,
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	if len(items) <= limit {
		return items, "", nil
	}
	page := items[:limit]
	return page, page[limit-1].Address, nil
}

func (r *investorRepo) Upsert(ctx context.Context, inv *models.Investor) error {
	return upsertByID(ctx, r.coll, inv.Address, inv)
}

type walletChallengeRepo struct{ coll *mongo.Collection }

func (r *walletChallengeRepo) Create(ctx context.Context, c *models.WalletChallenge) error {
	return createDoc(ctx, r.coll, c)
}

func (r *walletChallengeRepo) Get(ctx context.Context, id string) (*models.WalletChallenge, error) {
	return getByID[models.WalletChallenge](ctx, r.coll, id)
}

func (r *walletChallengeRepo) MarkUsed(ctx context.Context, id string) error {
	// The filter's "used": bson.M{"$ne": true} makes this a genuine
	// compare-and-swap: it only matches (and so only applies the $set) a
	// document that is not already used, so a concurrent second MarkUsed
	// for the same id cannot also succeed — see the interface doc comment.
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "used": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"used": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	// MatchedCount 0 means either "no such challenge" or "already used";
	// telling those apart needs one more read (this only happens on the
	// error path, never on the common success path above).
	if _, err := r.Get(ctx, id); err != nil {
		return err // repository.ErrNotFound
	}
	return repository.ErrAlreadyExists
}

// CountActive backs ChallengeService.Create's per-address active-challenge
// cap. Uses CountDocuments (not an approximate
// estimate) since this gates a security-relevant decision, not a UI
// display figure; the address+used+expiresAt compound index declared in
// EnsureIndexes (internal/dal/mongodb/mongodb.go) backs this exact
// query shape.
func (r *walletChallengeRepo) CountActive(ctx context.Context, address string, now time.Time) (int, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{"address": address, "used": false, "expiresAt": bson.M{"$gt": now}})
	return int(n), err
}

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

type kycVerificationRepo struct{ coll *mongo.Collection }

func (r *kycVerificationRepo) Upsert(ctx context.Context, v *models.KYCVerification) error {
	v.ID = models.KYCVerificationID(v.Provider, v.Ref)
	return upsertByID(ctx, r.coll, v.ID, v)
}

func (r *kycVerificationRepo) GetByRef(ctx context.Context, provider, ref string) (*models.KYCVerification, error) {
	return getByID[models.KYCVerification](ctx, r.coll, models.KYCVerificationID(provider, ref))
}

type complianceOperationRepo struct{ coll *mongo.Collection }

func (r *complianceOperationRepo) Create(ctx context.Context, op *models.ComplianceOperation) error {
	return createDoc(ctx, r.coll, op)
}

func (r *complianceOperationRepo) List(ctx context.Context) ([]*models.ComplianceOperation, error) {
	return findMany[models.ComplianceOperation](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
}
