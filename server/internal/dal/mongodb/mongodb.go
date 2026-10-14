// Package mongodb implements every internal/dal/repository interface against
// MongoDB. Its tests that need a live server skip unless MONGO_TEST_URI is set, so
// `go test ./...` runs without one. Other tests use internal/dal/memory, which
// implements the same interfaces.
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

// Collection names
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

// New builds a repository.Repositories backed by db
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

// EnsureIndexes creates the indexes the queries in this package rely on. It can
// be called on every startup.
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
	// unique per (chainId, blockNumber), so Record's upsert leaves one row per height
	// and PruneBefore's range delete has an index
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
	// payloadHash is not the _id, so replay protection in kycEventRepo.Create is atomic
	// only because of this unique index
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "payloadHash", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return err
	}
	// (provider, eventId) is a second replay-protection constraint: payloadHash catches
	// a byte-for-byte resend but not the same event serialized or signed differently.
	// The partial filter leaves out documents with an empty or missing eventId.
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "provider", Value: 1}, {Key: "eventId", Value: 1}},
		Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "eventId", Value: bson.D{{Key: "$gt", Value: ""}}}}),
	})
	if err != nil {
		return err
	}
	// address and occurredAt back the webhook-history screen's per-address listing,
	// most recent first
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "address", Value: 1}, {Key: "occurredAt", Value: -1}},
	})
	if err != nil {
		return err
	}
	// applyStatus backs ListPending and ListAppliedWithTx
	_, err = db.Collection(collKYCEvents).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "applyStatus", Value: 1}},
	})
	if err != nil {
		return err
	}
	// kyc_claims needs no index declared here, and neither does idempotency_keys for
	// its _id. kycClaim.Address and IdempotencyRecord.Key are the _id, and MongoDB
	// always keeps a unique index on _id. ClaimLatestForAddress and Reserve rely on it
	// for atomicity.
	//
	// The TTL index on idempotency_keys removes completed and abandoned reservations,
	// which would otherwise stay in the collection forever. A live reservation is
	// never removed early: Reserve compares expiresAt itself and takes over an expired
	// one atomically.
	_, err = db.Collection(collIdempotency).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return err
	}
	// wallet_challenges: any unauthenticated caller can create a challenge for a valid
	// address, and each call creates a document, so the collection has a TTL index and
	// a per-address cap. A TTL index with expireAfterSeconds 0 deletes a document once
	// its own expiresAt has passed. The second index backs CountActive: address and
	// used narrow the query to the few active challenges before the expiresAt range
	// check.
	_, err = db.Collection(collWalletChallenges).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		{Keys: bson.D{{Key: "address", Value: 1}, {Key: "used", Value: 1}, {Key: "expiresAt", Value: 1}}},
	})
	if err != nil {
		return err
	}
	// wallet_sessions and admin_challenges: the TTL index is background cleanup only.
	// walletSessionRepo.Get and adminChallengeRepo.Get check ExpiresAt themselves,
	// because the MongoDB TTL monitor runs about once every 60 seconds
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
	// purchases and redemption_requests: these compound indexes match ListPage's
	// (sort field descending, _id descending) query. Without them the results are the
	// same but come from a collection scan once the collection outgrows memory.
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
	// asset_records and transactions: the same, ascending, matching their oldest-first
	// order
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
	// (status, blockNumber) backs the 10-second status refresh: both branches of
	// ListUnfinalized and ListByStatuses, which uses the status prefix alone. Without
	// it they scan the whole transaction history.
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "blockNumber", Value: 1}},
	})
	if err != nil {
		return err
	}
	// replaces backs ListReplacements. The index is partial because only replacement
	// records have the field, and they are few.
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "replaces", Value: 1}},
		Options: options.Index().SetPartialFilterExpression(bson.D{{Key: "replaces", Value: bson.D{{Key: "$gt", Value: ""}}}}),
	})
	if err != nil {
		return err
	}
	return nil
}

// generic helpers shared by the repositories below

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

// createDocWithID inserts doc under a given _id, for collections whose Go struct
// has no field mapped to "_id", so bson tags cannot set a fixed id. It marshals doc
// to a bson.M and sets _id there, leaving the caller's struct unchanged. A
// duplicate key returns repository.ErrAlreadyExists, as in createDoc.
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

// keyset pagination helpers shared by the ListPage implementations (purchases,
// redemption requests, asset records, transactions)

// keysetFilter builds the filter for a descending sort, see keysetFilterDir
func keysetFilter(cursor, sortField string) bson.M {
	return keysetFilterDir(cursor, sortField, true)
}

// keysetFilterDir builds the keyset filter for either direction. A descending sort
// (purchases, redemption requests) uses $lt, and an ascending one (asset records,
// transactions) uses $gt. The query's sort and the compound index (see
// EnsureIndexes) must use the same direction.
//
// Use it only for a sort field stored as a number (purchases' blockNumber,
// redemption requests' int64 createdAt). A field stored from a time.Time is a BSON
// Date and needs keysetFilterDirDate.
func keysetFilterDir(cursor, sortField string, desc bool) bson.M {
	return keysetFilterOperand(cursor, sortField, desc, func(v int64) any { return v })
}

// keysetFilterDirDate is keysetFilterDir for a sort field stored from a time.Time,
// which is a BSON Date.
//
// MongoDB comparison operators match only values of the same BSON type, so
// {createdAt: {$gt: <int64>}} against a Date field matches nothing, and every page
// after the first would be empty. The cursor carries UnixNano int64 values, as
// KeysetCursor requires, and this converts that to a time.Time so the operand and
// the field have the same type. The conversion is exact because a BSON Date holds
// milliseconds and a value read from MongoDB is already aligned to them.
func keysetFilterDirDate(cursor, sortField string, desc bool) bson.M {
	return keysetFilterOperand(cursor, sortField, desc, func(v int64) any { return time.Unix(0, v).UTC() })
}

// keysetFilterOperand is the shared body of the two filters above. operand converts
// the cursor's int64 sort value to the BSON type the field stores
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

// trimKeysetPage turns a result fetched with SetLimit(limit+1), which every ListPage
// here does, into the page and the next cursor. If limit+1 rows came back there is
// another page, and the cursor is built from the last row returned, the one the
// next request resumes after.
func trimKeysetPage[T any](items []*T, limit int, extract func(*T) (sortValue int64, id string)) ([]*T, string, error) {
	if len(items) <= limit {
		return items, "", nil
	}
	page := items[:limit]
	sv, id := extract(page[limit-1])
	return page, repository.EncodeKeysetCursor(repository.KeysetCursor{SortValue: sv, ID: id}), nil
}
