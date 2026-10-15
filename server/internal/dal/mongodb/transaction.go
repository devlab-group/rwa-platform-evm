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
	txs, err := findMany[models.Transaction](ctx, r.coll, bson.M{"chainId": chainID, "txHash": txHash})
	if err != nil {
		return nil, err
	}
	if tx := preferSubmitted(txs); tx != nil {
		return tx, nil
	}
	return nil, repository.ErrNotFound
}

// preferSubmitted returns the first manager record, otherwise the first event-derived
// record, or nil for no matches, mirroring memory's deduplication preference
func preferSubmitted(txs []*models.Transaction) *models.Transaction {
	var match *models.Transaction
	for _, tx := range txs {
		if match == nil || (models.IsEventDerived(match) && !models.IsEventDerived(tx)) {
			match = tx
		}
	}
	return match
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

// UpdateConditional atomically replaces _id at expectedVersion. On no match, Get
// distinguishes ErrNotFound from a moved version, returned as (false, nil), as
// assetRecordRepo does.
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

func (r *transactionRepo) ListByStatuses(ctx context.Context, statuses ...models.TxStatus) ([]*models.Transaction, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	return findMany[models.Transaction](ctx, r.coll, bson.M{"status": bson.M{"$in": statuses}})
}

// ListUnfinalized applies the interface's work filter using both branches of the
// (status, blockNumber) index
func (r *transactionRepo) ListUnfinalized(ctx context.Context, confirmedFromBlock uint64) ([]*models.Transaction, error) {
	filter := bson.M{"$or": bson.A{
		bson.M{"status": bson.M{"$nin": bson.A{models.TxReplaced, models.TxFailed, models.TxConfirmed}}},
		bson.M{"status": models.TxConfirmed, "blockNumber": bson.M{"$gte": confirmedFromBlock}},
	}}
	return findMany[models.Transaction](ctx, r.coll, filter, options.Find().SetSort(bson.D{{Key: "submittedAt", Value: 1}}))
}

func (r *transactionRepo) ListReplacements(ctx context.Context) ([]*models.Transaction, error) {
	return findMany[models.Transaction](ctx, r.coll, bson.M{"replaces": bson.M{"$gt": ""}})
}

// ListPage queries (submittedAt, _id) ascending with keyset pagination and
// case-insensitive anchored From/To filtering
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
