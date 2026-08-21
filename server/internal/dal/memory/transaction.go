package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// --- transactions ---

type TransactionRepository struct {
	mu sync.RWMutex
	m  map[string]*models.Transaction
}

func NewTransactionRepository() *TransactionRepository {
	return &TransactionRepository{m: map[string]*models.Transaction{}}
}

func (r *TransactionRepository) Create(ctx context.Context, tx *models.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[tx.ID]; ok {
		return repository.ErrAlreadyExists
	}
	cp := *tx
	r.m[tx.ID] = &cp
	return nil
}

func (r *TransactionRepository) Get(ctx context.Context, id string) (*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (r *TransactionRepository) GetByIdempotencyKey(ctx context.Context, key string) (*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.m {
		if v.IdempotencyKey == key && key != "" {
			cp := *v
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *TransactionRepository) GetByTxHash(ctx context.Context, chainID int64, txHash string) (*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if txHash == "" {
		return nil, repository.ErrNotFound
	}
	var match *models.Transaction
	for _, v := range r.m {
		if v.ChainID != chainID || v.TxHash != txHash {
			continue
		}
		// Prefer a manager-submitted (non-EventDerived) record when several
		// share the hash, so the projector's dedup triggers deterministically.
		if match == nil || (!models.IsEventDerived(v) && models.IsEventDerived(match)) {
			cp := *v
			match = &cp
		}
	}
	if match == nil {
		return nil, repository.ErrNotFound
	}
	return match, nil
}

func (r *TransactionRepository) Update(ctx context.Context, tx *models.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[tx.ID]; !ok {
		return repository.ErrNotFound
	}
	cp := *tx
	r.m[tx.ID] = &cp
	return nil
}

func (r *TransactionRepository) UpdateConditional(ctx context.Context, tx *models.Transaction, expectedVersion int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.m[tx.ID]
	if !ok {
		return false, repository.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return false, nil
	}
	cp := *tx
	cp.Version = expectedVersion + 1
	r.m[tx.ID] = &cp
	return true, nil
}

func (r *TransactionRepository) List(ctx context.Context) ([]*models.Transaction, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Transaction, 0, len(r.m))
	for _, v := range r.m {
		cp := *v
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.Before(out[j].SubmittedAt) })
	return out, nil
}

func (r *TransactionRepository) ListByStatuses(ctx context.Context, statuses ...models.TxStatus) ([]*models.Transaction, error) {
	return r.filter(func(tx *models.Transaction) bool {
		for _, s := range statuses {
			if tx.Status == s {
				return true
			}
		}
		return false
	})
}

// ListUnfinalized mirrors transactionRepo's Mongo filter — see the
// interface doc comment.
func (r *TransactionRepository) ListUnfinalized(ctx context.Context, confirmedFromBlock uint64) ([]*models.Transaction, error) {
	return r.filter(func(tx *models.Transaction) bool {
		switch tx.Status {
		case models.TxReplaced, models.TxFailed:
			return false
		case models.TxConfirmed:
			return tx.BlockNumber >= confirmedFromBlock
		default:
			return true
		}
	})
}

func (r *TransactionRepository) ListReplacements(ctx context.Context) ([]*models.Transaction, error) {
	return r.filter(func(tx *models.Transaction) bool { return tx.Replaces != "" })
}

// filter returns the copies List would return, keeping only those keep
// accepts, in the same SubmittedAt order.
func (r *TransactionRepository) filter(keep func(*models.Transaction) bool) ([]*models.Transaction, error) {
	all, _ := r.List(context.Background())
	out := make([]*models.Transaction, 0)
	for _, v := range all {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// ListPage returns one bounded page — see the interface doc
// comment. Ascending-SubmittedAt-first, matching transactionRepo's mongodb
// sort; address, when non-empty, is matched case-insensitively against
// From OR To before the keyset walk (mirrors what api.listTransactions
// used to do in the handler).
func (r *TransactionRepository) ListPage(ctx context.Context, address, cursor string, limit int) ([]*models.Transaction, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addrLower := strings.ToLower(address)
	sorted := make([]*models.Transaction, 0, len(r.m))
	for _, v := range r.m {
		if addrLower != "" && strings.ToLower(v.From) != addrLower && strings.ToLower(v.To) != addrLower {
			continue
		}
		cp := *v
		sorted = append(sorted, &cp)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].SubmittedAt.Equal(sorted[j].SubmittedAt) {
			return sorted[i].SubmittedAt.Before(sorted[j].SubmittedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	page, next := repository.KeysetPage(sorted,
		func(tx *models.Transaction) int64 { return tx.SubmittedAt.UnixNano() },
		func(tx *models.Transaction) string { return tx.ID },
		cursor, limit, false)
	return page, next, nil
}
