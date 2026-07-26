package mongodb

import (
	"context"
	"errors"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type transactionRepo struct{ coll *mongo.Collection }

func (r *transactionRepo) Create(ctx context.Context, tx *models.Transaction) error {
	return createDoc(ctx, r.coll, tx)
}

func (r *transactionRepo) Get(ctx context.Context, id string) (*models.Transaction, error) {
	return getByID[models.Transaction](ctx, r.coll, id)
}

func (r *transactionRepo) GetByIdempotencyKey(ctx context.Context, key string) (*models.Transaction, error) {
	if key == "" {
		return nil, repository.ErrNotFound
	}
	return findOne[models.Transaction](ctx, r.coll, bson.M{"idempotencyKey": key})
}

func (r *transactionRepo) GetByTxHash(ctx context.Context, chainID int64, txHash string) (*models.Transaction, error) {
	if txHash == "" {
		return nil, repository.ErrNotFound
	}
	// Sort by _id so a manager-submitted record (whose id does NOT start with
	// the "evt:" prefix, which sorts after most hex/hyphenated ids) is
	// returned ahead of any coexisting event-derived record, keeping the
	// projector's dedup deterministic — mirrors the memory adapter.
	return findOne[models.Transaction](ctx, r.coll,
		bson.M{"chainId": chainID, "txHash": txHash},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: 1}}))
}

func (r *transactionRepo) Update(ctx context.Context, tx *models.Transaction) error {
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": tx.ID}, tx)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// UpdateConditional replaces tx only if the document currently stored under
// _id=tx.ID has version==expectedVersion — the filter's match/no-match IS
// the atomicity (same pattern as idempotencyRepo.Reserve/nonceLeaseRepo.
// Acquire above): MatchedCount 0 means either the id doesn't exist at all
// or a concurrent writer already advanced version past what this caller
// last read, and this method cannot tell those apart from the ReplaceOne
// result alone, so it re-Gets to distinguish "not found" (returns
// ErrNotFound) from "version moved" (returns false, nil — a lost race, not
// an error) for the caller.
func (r *transactionRepo) UpdateConditional(ctx context.Context, tx *models.Transaction, expectedVersion int) (bool, error) {
	cp := *tx
	cp.Version = expectedVersion + 1
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": tx.ID, "version": expectedVersion}, cp)
	if err != nil {
		return false, err
	}
	if res.MatchedCount > 0 {
		return true, nil
	}
	if _, err := r.Get(ctx, tx.ID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, repository.ErrNotFound
		}
		return false, err
	}
	return false, nil
}

func (r *transactionRepo) List(ctx context.Context) ([]*models.Transaction, error) {
	return findMany[models.Transaction](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "submittedAt", Value: 1}}))
}

func (r *transactionRepo) ListByStatus(ctx context.Context, status models.TxStatus) ([]*models.Transaction, error) {
	return findMany[models.Transaction](ctx, r.coll, bson.M{"status": status})
}

// ListPage returns one bounded, repository-level keyset page — see
// the interface doc comment. Sorted (submittedAt asc, _id asc), matching
// List's pre-existing order. address, when non-empty, is matched
// case-insensitively against From OR To directly in the query (a
// case-insensitive regex anchor, mirroring the exact
// strings.ToLower(...) comparison api.listTransactions used to do in the
// handler) rather than fetched-then-filtered in Go.
func (r *transactionRepo) ListPage(ctx context.Context, address, cursor string, limit int) ([]*models.Transaction, string, error) {
	clauses := bson.A{keysetFilterDirDate(cursor, "submittedAt", false)}
	if address != "" {
		pattern := "^" + regexp.QuoteMeta(address) + "$"
		clauses = append(clauses, bson.M{"$or": bson.A{
			bson.M{"from": bson.M{"$regex": pattern, "$options": "i"}},
			bson.M{"to": bson.M{"$regex": pattern, "$options": "i"}},
		}})
	}
	items, err := findMany[models.Transaction](ctx, r.coll, bson.M{"$and": clauses},
		options.Find().SetSort(bson.D{{Key: "submittedAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(tx *models.Transaction) (int64, string) { return tx.SubmittedAt.UnixNano(), tx.ID })
}

type chainEventRepo struct{ coll *mongo.Collection }

func eventFilter(k models.EventKey) bson.M {
	return bson.M{"chainId": k.ChainID, "address": k.Address, "txHash": k.TxHash, "logIndex": k.LogIndex}
}

func (r *chainEventRepo) Exists(ctx context.Context, key models.EventKey) (bool, error) {
	n, err := r.coll.CountDocuments(ctx, eventFilter(key))
	return n > 0, err
}

func (r *chainEventRepo) Create(ctx context.Context, e *models.ChainEvent) error {
	err := createDoc(ctx, r.coll, e)
	if err == repository.ErrAlreadyExists {
		return nil // idempotent: matches memory repo semantics (re-ingestion is a no-op)
	}
	return err
}

func (r *chainEventRepo) DeleteFromBlock(ctx context.Context, chainID int64, address string, fromBlock uint64) (int, error) {
	res, err := r.coll.DeleteMany(ctx, bson.M{"chainId": chainID, "address": address, "blockNumber": bson.M{"$gte": fromBlock}})
	if err != nil {
		return 0, err
	}
	return int(res.DeletedCount), nil
}

func (r *chainEventRepo) ListByName(ctx context.Context, chainID int64, address, name string) ([]*models.ChainEvent, error) {
	return findMany[models.ChainEvent](ctx, r.coll,
		bson.M{"chainId": chainID, "address": address, "name": name},
		options.Find().SetSort(bson.D{{Key: "blockNumber", Value: 1}, {Key: "logIndex", Value: 1}}),
	)
}

func (r *chainEventRepo) ListAll(ctx context.Context, chainID int64) ([]*models.ChainEvent, error) {
	return findMany[models.ChainEvent](ctx, r.coll,
		bson.M{"chainId": chainID},
		options.Find().SetSort(bson.D{{Key: "blockNumber", Value: 1}, {Key: "logIndex", Value: 1}, {Key: "txHash", Value: 1}}),
	)
}

type indexerCheckpointRepo struct{ coll *mongo.Collection }

func (r *indexerCheckpointRepo) Get(ctx context.Context, chainID int64, address string) (*models.IndexerCheckpoint, error) {
	return findOne[models.IndexerCheckpoint](ctx, r.coll, bson.M{"chainId": chainID, "address": address})
}

func (r *indexerCheckpointRepo) Set(ctx context.Context, c *models.IndexerCheckpoint) error {
	_, err := r.coll.ReplaceOne(ctx, bson.M{"chainId": c.ChainID, "address": c.Address}, c, options.Replace().SetUpsert(true))
	return err
}

// blockHashDoc is the indexer_block_hashes collection's document shape
// (see repository.BlockHashRepository's doc comment).
type blockHashDoc struct {
	ChainID     int64  `bson:"chainId"`
	BlockNumber uint64 `bson:"blockNumber"`
	Hash        string `bson:"hash"`
}

type blockHashRepo struct{ coll *mongo.Collection }

func blockHashFilter(chainID int64, blockNumber uint64) bson.M {
	return bson.M{"chainId": chainID, "blockNumber": blockNumber}
}

func (r *blockHashRepo) Record(ctx context.Context, chainID int64, blockNumber uint64, hash string) error {
	_, err := r.coll.ReplaceOne(ctx, blockHashFilter(chainID, blockNumber),
		blockHashDoc{ChainID: chainID, BlockNumber: blockNumber, Hash: hash}, options.Replace().SetUpsert(true))
	return err
}

func (r *blockHashRepo) Get(ctx context.Context, chainID int64, blockNumber uint64) (string, error) {
	doc, err := findOne[blockHashDoc](ctx, r.coll, blockHashFilter(chainID, blockNumber))
	if err != nil {
		return "", err
	}
	return doc.Hash, nil
}

func (r *blockHashRepo) PruneBefore(ctx context.Context, chainID int64, keepFrom uint64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"chainId": chainID, "blockNumber": bson.M{"$lt": keepFrom}})
	return err
}

// nonceLeaseRepo persists nonce_leases (distributed multi-replica nonce
// coordination — see models.NonceLease's doc comment).
//
// Deliberately no TTL index on expiresAt: EnsureIndexes does not register
// one for this collection, and Acquire/Renew/Release below never rely on
// (or benefit from) Mongo auto-deleting an expired document — an
// auto-delete would let a subsequent Acquire's $ifNull-based token
// increment reset back to 1, breaking the strict-monotonicity guarantee
// the whole fencing scheme depends on (see models.NonceLease). The
// collection stays tiny regardless (one document per configured hot key).
type nonceLeaseRepo struct{ coll *mongo.Collection }

// Acquire uses the same "the filter's match/no-match IS the atomicity"
// pattern as idempotencyRepo.Reserve (see its doc comment): the filter
// matches either no document at all (first-ever Acquire for key) or an
// EXPIRED one, and an aggregation-pipeline update lets the new token be
// computed FROM the (possibly absent) existing one in the same atomic
// operation ($ifNull($token, 0) + 1) — a plain $set update could not
// express "increment relative to whatever's already there, defaulting to
// 0" without a separate read. A LIVE (non-expired) document under _id=key
// does not match the filter, so the upsert instead attempts an insert that
// collides with that existing _id and fails with a duplicate-key error —
// resolved below as simply "not acquired," exactly mirroring Reserve's
// handling of the same race.
func (r *nonceLeaseRepo) Acquire(ctx context.Context, key, holderID string, ttl time.Duration) (uint64, bool, error) {
	now := time.Now().UTC()
	filter := bson.M{"_id": key, "expiresAt": bson.M{"$lt": now}}
	pipeline := mongo.Pipeline{
		{{Key: "$set", Value: bson.M{
			"holderId":  holderID,
			"token":     bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$token", 0}}, 1}},
			"expiresAt": now.Add(ttl),
			"updatedAt": now,
		}}},
	}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var out models.NonceLease
	err := r.coll.FindOneAndUpdate(ctx, filter, pipeline, opts).Decode(&out)
	switch {
	case err == nil:
		return out.Token, true, nil
	case mongo.IsDuplicateKeyError(err):
		return 0, false, nil // held live by someone else
	default:
		return 0, false, err
	}
}

func (r *nonceLeaseRepo) Renew(ctx context.Context, key, holderID string, token uint64, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": key, "holderId": holderID, "token": token},
		bson.M{"$set": bson.M{"expiresAt": now.Add(ttl), "updatedAt": now}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// Release marks the document immediately expired (via $set, not $delete) —
// see memory.NonceLeaseRepository.Release's doc comment for why deleting
// it would reset Acquire's token counter and reintroduce the exact
// ABA-style fencing-token collision this whole mechanism exists to
// prevent.
func (r *nonceLeaseRepo) Release(ctx context.Context, key, holderID string, token uint64) error {
	now := time.Now().UTC()
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": key, "holderId": holderID, "token": token},
		bson.M{"$set": bson.M{"expiresAt": now, "updatedAt": now}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrFencingTokenMismatch
	}
	return nil
}

// publicationRepo persists ipfs_publications.
type publicationRepo struct{ coll *mongo.Collection }

func (r *publicationRepo) Get(ctx context.Context, id string) (*models.PublicationRecord, error) {
	return getByID[models.PublicationRecord](ctx, r.coll, id)
}

func (r *publicationRepo) Upsert(ctx context.Context, rec *models.PublicationRecord) error {
	return upsertByID(ctx, r.coll, rec.ID, rec)
}

func (r *publicationRepo) List(ctx context.Context) ([]*models.PublicationRecord, error) {
	return findMany[models.PublicationRecord](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
}

// deadLetterRepo persists indexer_dead_letters (see
// repository.DeadLetterRepository's doc comment on Record's
// upsert-with-increment semantics).
type deadLetterRepo struct{ coll *mongo.Collection }

func (r *deadLetterRepo) Record(ctx context.Context, e *models.DeadLetterEntry) error {
	existing, err := findOne[models.DeadLetterEntry](ctx, r.coll, bson.M{"_id": e.ID})
	if err != nil && err != repository.ErrNotFound {
		return err
	}
	cp := *e
	if existing != nil {
		cp.FirstFailedAt = existing.FirstFailedAt
		cp.RetryCount = existing.RetryCount + 1
		cp.Resolved = false
	}
	_, err = r.coll.ReplaceOne(ctx, bson.M{"_id": e.ID}, cp, options.Replace().SetUpsert(true))
	return err
}

func (r *deadLetterRepo) Get(ctx context.Context, id string) (*models.DeadLetterEntry, error) {
	return getByID[models.DeadLetterEntry](ctx, r.coll, id)
}

func (r *deadLetterRepo) List(ctx context.Context) ([]*models.DeadLetterEntry, error) {
	return findMany[models.DeadLetterEntry](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "firstFailedAt", Value: 1}}))
}

func (r *deadLetterRepo) Resolve(ctx context.Context, id string) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"resolved": true}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}
