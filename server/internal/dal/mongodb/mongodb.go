// Package mongodb implements every internal/dal/repository interface against
// MongoDB. It is exercised by integration/E2E
// environments with a live Mongo instance; `go test ./...` never requires
// one (see internal/dal/memory for the interface-identical fakes
// unit tests use instead).
package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/repository"
)

// Collection names.
const (
	collProjects             = "projects"
	collAssetProfiles        = "asset_profiles"
	collAssetRecords         = "asset_records"
	collAuditPackages        = "audit_packages"
	collAttestations         = "attestations"
	collInvestors            = "investors"
	collWalletChallenges     = "wallet_challenges"
	collKYCEvents            = "kyc_events"        // append-only webhook decision history
	collKYCClaims            = "kyc_claims"        // one doc per address: current (occurredAt,eventKey) claim winner
	collKYCVerifications     = "kyc_verifications" // (provider,ref) -> wallet address, written at verification start
	collComplianceOperations = "compliance_operations"
	collTransactions         = "transactions"
	collChainEvents          = "chain_events"
	collPurchases            = "purchases"
	collRedemptionRequests   = "redemption_requests"
	collAuditLogs            = "audit_logs"
	collIndexerCheckpoints   = "indexer_checkpoints"
	collIndexerBlockHashes   = "indexer_block_hashes"
	collIndexerDeadLetters   = "indexer_dead_letters"
	collIdempotency          = "idempotency_keys"
	collPublications         = "ipfs_publications"
	collNonceLeases          = "nonce_leases"
	collWalletSessions       = "wallet_sessions"
	collAdminChallenges      = "admin_challenges"
)

// New builds a repository.Repositories backed by db.
func New(db *mongo.Database) *repository.Repositories {
	return &repository.Repositories{
		Projects:             &projectRepo{coll: db.Collection(collProjects)},
		AssetProfiles:        &assetProfileRepo{coll: db.Collection(collAssetProfiles)},
		AssetRecords:         &assetRecordRepo{coll: db.Collection(collAssetRecords)},
		AuditPackages:        &auditPackageRepo{coll: db.Collection(collAuditPackages)},
		Attestations:         &attestationRepo{coll: db.Collection(collAttestations)},
		Investors:            &investorRepo{coll: db.Collection(collInvestors)},
		WalletChallenges:     &walletChallengeRepo{coll: db.Collection(collWalletChallenges)},
		KYCEvents:            &kycEventRepo{coll: db.Collection(collKYCEvents), claims: db.Collection(collKYCClaims)},
		KYCVerifications:     &kycVerificationRepo{coll: db.Collection(collKYCVerifications)},
		ComplianceOperations: &complianceOperationRepo{coll: db.Collection(collComplianceOperations)},
		Transactions:         &transactionRepo{coll: db.Collection(collTransactions)},
		ChainEvents:          &chainEventRepo{coll: db.Collection(collChainEvents)},
		Purchases:            &purchaseRepo{coll: db.Collection(collPurchases)},
		RedemptionRequests:   &redemptionRequestRepo{coll: db.Collection(collRedemptionRequests)},
		AuditLogs:            &auditLogRepo{coll: db.Collection(collAuditLogs)},
		IndexerCheckpoints:   &indexerCheckpointRepo{coll: db.Collection(collIndexerCheckpoints)},
		IndexerBlockHashes:   &blockHashRepo{coll: db.Collection(collIndexerBlockHashes)},
		IndexerChunks: &chainChunkRepo{
			db: db, events: db.Collection(collChainEvents),
			blockHashes: db.Collection(collIndexerBlockHashes), checkpoints: db.Collection(collIndexerCheckpoints),
		},
		IndexerDeadLetters: &deadLetterRepo{coll: db.Collection(collIndexerDeadLetters)},
		Idempotency:        &idempotencyRepo{coll: db.Collection(collIdempotency)},
		Publications:       &publicationRepo{coll: db.Collection(collPublications)},
		NonceLeases:        &nonceLeaseRepo{coll: db.Collection(collNonceLeases)},
		WalletSessions:     &walletSessionRepo{coll: db.Collection(collWalletSessions)},
		AdminChallenges:    &adminChallengeRepo{coll: db.Collection(collAdminChallenges)},
	}
}

// EnsureIndexes creates the indexes the query patterns above rely on.
// Idempotent: safe to call on every startup.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	_, err := db.Collection(collChainEvents).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "chainId", Value: 1}, {Key: "address", Value: 1}, {Key: "txHash", Value: 1}, {Key: "logIndex", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "chainId", Value: 1}, {Key: "address", Value: 1}, {Key: "name", Value: 1}}},
		{Keys: bson.D{{Key: "chainId", Value: 1}, {Key: "address", Value: 1}, {Key: "blockNumber", Value: 1}}},
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collIndexerCheckpoints).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "chainId", Value: 1}, {Key: "address", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return err
	}
	// Unique per (chainId,blockNumber) so Record's upsert never leaves
	// duplicate rows for the same height, and so PruneBefore's range
	// delete has an index to use.
	_, err = db.Collection(collIndexerBlockHashes).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "chainId", Value: 1}, {Key: "blockNumber", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "idempotencyKey", Value: 1}},
		Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "idempotencyKey", Value: bson.D{{Key: "$gt", Value: ""}}}}),
	})
	if err != nil {
		return err
	}
	// payloadHash has no explicit uniqueness guarantee otherwise: it's a
	// plain field, not the collection's _id, so replay protection
	// (compliance.WebhookService.Process, via kycEventRepo.Create) is only
	// actually atomic once this index exists — see EnsureIndexes' doc
	// comment on idempotency_keys below for the collection that does NOT
	// need one of these.
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "payloadHash", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return err
	}
	// (provider,eventId) is the SECOND, independent replay-protection
	// constraint: payloadHash alone catches a byte-for-byte
	// resend, but not the same logical decision reserialized/re-signed as
	// different bytes. A partial filter excludes legacy documents with an
	// empty eventId (predating the field) from the uniqueness constraint,
	// same pattern as idempotencyKey below.
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "provider", Value: 1}, {Key: "eventId", Value: 1}},
		Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "eventId", Value: bson.D{{Key: "$gt", Value: ""}}}}),
	})
	if err != nil {
		return err
	}
	// address+occurredAt backs the webhook-history screen's per-address,
	// most-recent-first listing (the per-address freshness DECISION itself
	// is no longer made by querying this index — see kyc_claims below —
	// this is purely a read-path index now).
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "address", Value: 1}, {Key: "occurredAt", Value: -1}},
	})
	if err != nil {
		return err
	}
	// applyStatus backs ListPending, the reconciler's work-queue query.
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "applyStatus", Value: 1}},
	})
	if err != nil {
		return err
	}
	// kyc_claims deliberately has NO explicit index declared here, same
	// reasoning as idempotency_keys' _id below: kycClaim.Address is bson
	// "_id" (kycEventRepo.ClaimLatestForAddress/CurrentClaimEventKey), so
	// the automatic unique _id index is exactly the index the CAS in
	// ClaimLatestForAddress relies on.
	//
	// idempotency_keys' _id uniqueness needs no explicit index declaration:
	// models.IdempotencyRecord.Key is bson "_id" (see idempotencyRepo.Reserve),
	// and MongoDB unconditionally maintains a unique index on every
	// collection's _id automatically. Reserve's atomicity (internal/
	// repository/repository.go's IdempotencyRepository doc comment) comes
	// from that automatic index plus FindOneAndReplace's upsert-filtered-on-
	// expiry pattern. What IS explicit below is the TTL index: without one,
	// a completed (or permanently abandoned) reservation sits in the
	// collection forever: without a TTL index a crashed pending request stays
	// wedged in the collection. This TTL index handles the storage side; the
	// "returns forever" behavior is handled separately by Reserve's
	// upsert-filtered-on-expiry pattern above.
	_, err = db.Collection(collIdempotency).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return err
	}
	// wallet_challenges: any unauthenticated caller can create a challenge
	// for any valid address, and every call makes a new nonce/document, so
	// these need both a TTL sweep and a per-address cap to bound growth.
	// SetExpireAfterSeconds(0) deletes a document once its OWN expiresAt
	// value is in the past, same pattern as idempotency_keys above. The
	// second index backs ChallengeService.Create's per-address active-
	// challenge cap (WalletChallengeRepository.CountActive): address+used
	// narrows to the small "still active" subset before the expiresAt
	// range check, matching the query CountActive actually issues.
	_, err = db.Collection(collWalletChallenges).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		{Keys: bson.D{{Key: "address", Value: 1}, {Key: "used", Value: 1}, {Key: "expiresAt", Value: 1}}},
	})
	if err != nil {
		return err
	}
	// wallet_sessions / admin_challenges: TTL indexes are a background cleanup
	// convenience here, not the correctness mechanism — walletSessionRepo.Get
	// and adminChallengeRepo.Get always check ExpiresAt themselves before
	// Mongo's own ~60s TTL sweep would otherwise get to it, same reasoning as
	// idempotency_keys' TTL index above.
	_, err = db.Collection(collWalletSessions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collAdminChallenges).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return err
	}
	// purchases / redemption_requests: these compound
	// indexes back ListPage's (sortField desc, _id desc) query pattern
	// directly — without them the keyset filter/sort still returns
	// correct results, just via a collection scan instead of an index
	// seek once a collection grows past what fits comfortably in memory.
	_, err = db.Collection(collPurchases).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "blockNumber", Value: -1}, {Key: "_id", Value: -1}},
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collRedemptionRequests).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}},
	})
	if err != nil {
		return err
	}
	// asset_records / transactions: same ListPage-backing
	// purpose as above, ASCENDING to match those two collections'
	// pre-existing oldest-first order.
	_, err = db.Collection(collAssetRecords).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}},
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "submittedAt", Value: 1}, {Key: "_id", Value: 1}},
	})
	if err != nil {
		return err
	}
	// (status, blockNumber) backs the every-10s refresh tick:
	// ListUnfinalized's two branches (status $nin, and Confirmed still
	// inside its reorg-recheck window) and ListByStatuses, which uses the
	// status prefix alone. Without it both are collection scans that grow
	// with the deployment's whole transaction history.
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "blockNumber", Value: 1}},
	})
	if err != nil {
		return err
	}
	// replaces backs ListReplacements. Partial: only replacement records
	// carry the field at all, and they are a small minority.
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "replaces", Value: 1}},
		Options: options.Index().SetPartialFilterExpression(bson.D{{Key: "replaces", Value: bson.D{{Key: "$gt", Value: ""}}}}),
	})
	if err != nil {
		return err
	}
	return nil
}

// --- generic helpers shared by every repo below ---

func getByID[T any](ctx context.Context, coll *mongo.Collection, id any) (*T, error) {
	var out T
	err := coll.FindOne(ctx, bson.M{"_id": id}).Decode(&out)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func upsertByID(ctx context.Context, coll *mongo.Collection, id any, doc any) error {
	_, err := coll.ReplaceOne(ctx, bson.M{"_id": id}, doc, options.Replace().SetUpsert(true))
	return err
}

func createDoc(ctx context.Context, coll *mongo.Collection, doc any) error {
	_, err := coll.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return repository.ErrAlreadyExists
	}
	return err
}

// createDocWithID inserts doc under a caller-chosen _id, for collections
// (like projects — see projectRepo) whose Go struct does not map any field
// to "_id" and therefore cannot express a fixed technical id via bson tags
// alone. Re-marshals doc to bson.M rather than mutating the caller's struct,
// so this stays generic over any doc type. Duplicate-key (someone already
// holds this _id) maps to repository.ErrAlreadyExists, same as createDoc.
func createDocWithID(ctx context.Context, coll *mongo.Collection, id any, doc any) error {
	raw, err := bson.Marshal(doc)
	if err != nil {
		return err
	}
	var m bson.M
	if err := bson.Unmarshal(raw, &m); err != nil {
		return err
	}
	m["_id"] = id
	_, err = coll.InsertOne(ctx, m)
	if mongo.IsDuplicateKeyError(err) {
		return repository.ErrAlreadyExists
	}
	return err
}

func findOne[T any](ctx context.Context, coll *mongo.Collection, filter any, opts ...options.Lister[options.FindOneOptions]) (*T, error) {
	var out T
	err := coll.FindOne(ctx, filter, opts...).Decode(&out)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func findMany[T any](ctx context.Context, coll *mongo.Collection, filter any, opts ...options.Lister[options.FindOptions]) ([]*T, error) {
	cur, err := coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []*T{}
	for cur.Next(ctx) {
		var v T
		if err := cur.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, cur.Err()
}

// --- keyset pagination helpers, shared by every ListPage implementation
// below (purchases, redemption requests, asset records, transactions) ---

// keysetFilter builds the descending-sort $or filter — see
// keysetFilterDir's doc comment.
func keysetFilter(cursor, sortField string) bson.M {
	return keysetFilterDir(cursor, sortField, true)
}

// keysetFilterDir is keysetFilter generalized over sort direction: desc
// (the newest-first collections — purchases, redemption requests) uses
// $lt/descending; !desc (the oldest-first ones — asset records,
// transactions, which keep their pre-existing ascending order) uses
// $gt/ascending. Both still require the SAME direction's compound
// index (see EnsureIndexes) and the caller's own SetSort to match.
//
// Use this only for a sort field persisted as a NUMBER (purchases'
// blockNumber, redemption requests' int64 createdAt). A field persisted from a
// Go time.Time is a BSON Date, and needs keysetFilterDirDate — see its doc.
func keysetFilterDir(cursor, sortField string, desc bool) bson.M {
	return keysetFilterOperand(cursor, sortField, desc, func(v int64) any { return v })
}

// keysetFilterDirDate is keysetFilterDir for a sort field persisted from a Go
// time.Time, i.e. stored as a BSON Date rather than a number.
//
// The distinction is not cosmetic: MongoDB comparison operators are
// type-bracketed, so {createdAt: {$gt: <int64>}} against a Date-typed field
// matches NOTHING — every page after the first would come back empty while the
// first page (which has no cursor and so no filter at all) looked fine. The
// cursor still carries UnixNano int64s, as KeysetCursor requires; this converts
// that back to a time.Time so the operand and the stored value are the same
// BSON type. The round trip is exact: BSON Dates hold milliseconds, so a value
// read back out of Mongo is already millisecond-aligned before UnixNano() ever
// sees it.
func keysetFilterDirDate(cursor, sortField string, desc bool) bson.M {
	return keysetFilterOperand(cursor, sortField, desc, func(v int64) any { return time.Unix(0, v).UTC() })
}

// keysetFilterOperand is the shared body of the two filters above; operand maps
// the cursor's int64 sort value to whatever BSON type the stored field uses.
func keysetFilterOperand(cursor, sortField string, desc bool, operand func(int64) any) bson.M {
	c, ok := repository.DecodeKeysetCursor(cursor)
	if !ok {
		return bson.M{}
	}
	op := "$lt"
	if !desc {
		op = "$gt"
	}
	sv := operand(c.SortValue)
	return bson.M{"$or": bson.A{
		bson.M{sortField: bson.M{op: sv}},
		bson.M{sortField: sv, "_id": bson.M{op: c.ID}},
	}}
}

// trimKeysetPage turns a query result fetched with SetLimit(limit+1) (the
// convention every ListPage call site above uses) into the (page,
// nextCursor) pair the repository.*Repository.ListPage interfaces return:
// a full limit+1 rows means there IS a further page, encoded from the
// LAST item actually returned (not the trimmed (limit+1)th) — exactly the
// item a caller's next request should resume strictly after.
func trimKeysetPage[T any](items []*T, limit int, extract func(*T) (sortValue int64, id string)) ([]*T, string, error) {
	if len(items) <= limit {
		return items, "", nil
	}
	page := items[:limit]
	sv, id := extract(page[limit-1])
	return page, repository.EncodeKeysetCursor(repository.KeysetCursor{SortValue: sv, ID: id}), nil
}
