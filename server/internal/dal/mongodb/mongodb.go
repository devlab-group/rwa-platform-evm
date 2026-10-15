// Package mongodb implements repository interfaces against MongoDB. Live tests require
// MONGO_TEST_URI and otherwise skip; memory provides database-free workflow tests.
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
	collBurns                = "burns"
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
		Burns:                &burnRepo{coll: db.Collection(collBurns)},
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
	// The unique (provider, eventId) index catches reserialized deliveries missed by
	// payloadHash. Its partial filter excludes empty or missing eventId values.
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
	// kyc_claims and idempotency_keys use MongoDB's unique _id index for conditional
	// claims. The idempotency TTL index cleans up completed or abandoned expired rows;
	// Reserve independently checks expiry for atomic takeover.
	_, err = db.Collection(collIdempotency).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return err
	}
	// Public wallet challenges use expiresAt TTL cleanup. The (address, used, expiresAt)
	// index supports CountActive's pre-insert limit check; expiry cleanup is asynchronous.
	_, err = db.Collection(collWalletChallenges).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		{Keys: bson.D{{Key: "address", Value: 1}, {Key: "used", Value: 1}, {Key: "expiresAt", Value: 1}}},
	})
	if err != nil {
		return err
	}
	// Wallet-session and admin-challenge Get methods enforce ExpiresAt directly; TTL
	// indexes perform later background cleanup
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
	// Purchase and redemption compound indexes match ListPage's descending sort field and
	// _id, avoiding collection scans and in-memory sorting
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
	// Burn indexes support pagination, per-record totals and unique operationId lookup
	_, err = db.Collection(collBurns).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}},
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collBurns).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "recordId", Value: 1}},
	})
	if err != nil {
		return err
	}
	_, err = db.Collection(collBurns).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "operationId", Value: 1}}, Options: options.Index().SetUnique(true),
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
	// The (status, blockNumber) index serves both ListUnfinalized branches and
	// ListByStatuses through its status prefix, avoiding history-wide scans
	_, err = db.Collection(collTransactions).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "blockNumber", Value: 1}},
	})
	if err != nil {
		return err
	}
	// The partial replaces index serves ListReplacements without indexing non-replacement
	// rows
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

// createDocWithID inserts a fixed _id for structs without an _id BSON field. It
// marshals a copy to bson.M, leaving the struct unchanged; duplicate keys return
// repository.ErrAlreadyExists.
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

// keysetFilterDir compares numeric sort fields with $lt descending or $gt ascending,
// matching query sort and compound-index direction. Use keysetFilterDirDate for
// time.Time/BSON Date fields; purchases.blockNumber and redemption_requests.createdAt
// use numeric operands.
func keysetFilterDir(cursor, sortField string, desc bool) bson.M {
	return keysetFilterOperand(cursor, sortField, desc, func(v int64) any { return v })
}

// keysetFilterDirDate converts cursor UnixNano to time.Time for BSON Date comparisons;
// int64 operands would empty later pages. Stored Dates are millisecond-aligned,
// preserving the round trip.
func keysetFilterDirDate(cursor, sortField string, desc bool) bson.M {
	return keysetFilterOperand(cursor, sortField, desc, func(v int64) any { return time.Unix(0, v).UTC() })
}

// keysetFilterOperand shares cursor filtering; operand converts the sort value to its
// stored BSON type
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

// trimKeysetPage trims limit+1 query results to limit. An extra row yields a cursor
// from the returned page's last item; otherwise no cursor is needed.
func trimKeysetPage[T any](items []*T, limit int, extract func(*T) (sortValue int64, id string)) ([]*T, string, error) {
	if len(items) <= limit {
		return items, "", nil
	}
	page := items[:limit]
	sv, id := extract(page[limit-1])
	return page, repository.EncodeKeysetCursor(repository.KeysetCursor{SortValue: sv, ID: id}), nil
}
