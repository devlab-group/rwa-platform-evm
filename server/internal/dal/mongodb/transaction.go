package mongodb

import (
	"context"
	"errors"
	"regexp"

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
