// Package keeper polls the chain and settles forward-priced purchase orders.
// Settlement is permissionless; use a dedicated wallet with no roles, funded for gas.
// The keeper reads the chain directly and can run while the platform is down.
package keeper

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

const (
	// replaceAfter is how long a transaction may stay unmined before its fees are raised
	replaceAfter       = 5 * time.Minute
	replaceBumpPercent = 20
	confirmations      = 1
	// orderPending is the Vault's PurchaseStatus.Pending
	orderPending = 1
)

// Keeper tracks open orders between passes. It is not safe for concurrent use
type Keeper struct {
	client  blockchain.Client
	txs     blockchain.TxManager
	records repository.TransactionRepository
	signer  blockchain.Signer
	vault   common.Address
	binding bindings.Vault
	now     func() time.Time

	next uint64            // first order id not yet seen
	open []uint64          // seen ids that may still settle, oldest first
	sent map[uint64]string // order id to its latest transaction record
}

// New returns a Keeper that settles orders on vault with signer
func New(client blockchain.Client, signer blockchain.Signer, chainID *big.Int, vault common.Address, caps blockchain.FeeCaps) *Keeper {
	// In-memory records are lost on restart, so an unmined order may receive another settlement
	// transaction. Duplicate settlements can spend gas on reverts; pending chain nonces still
	// determine the next transaction nonce.
	records := memory.NewTransactionRepository()
	return &Keeper{
		client:  client,
		txs:     blockchain.NewTxManagerWithFeeCaps(client, records, chainID, blockchain.FeeModeEIP1559, caps),
		records: records,
		signer:  signer,
		vault:   vault,
		binding: bindings.NewVault(),
		now:     time.Now,
		sent:    map[uint64]string{},
	}
}

// Pass sends settlePurchase for each Pending order that would succeed now. A failed order
// does not stop the others: Pass tries it again next time and returns the joined errors.
func (k *Keeper) Pass(ctx context.Context) error {
	var errs []error
	if err := k.txs.RefreshStatuses(ctx, confirmations); err != nil {
		errs = append(errs, fmt.Errorf("refresh transactions: %w", err))
	}
	if err := k.discover(ctx); err != nil {
		errs = append(errs, fmt.Errorf("read nextPurchaseId: %w", err))
	}
	open := k.open[:0]
	for _, id := range k.open {
		stillOpen, err := k.settle(ctx, id)
		if stillOpen {
			open = append(open, id)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("order %d: %w", id, err))
		}
	}
	k.open = open
	return errors.Join(errs...)
}

// Run calls Pass every interval until ctx ends, logging failures
func (k *Keeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := k.Pass(ctx); err != nil && ctx.Err() == nil {
				log.Printf("keeper: %v", err)
			}
		}
	}
}

func (k *Keeper) discover(ctx context.Context) error {
	data, err := k.binding.PackNextPurchaseId()
	if err != nil {
		return err
	}
	raw, err := blockchain.Call(ctx, k.client, k.vault, data)
	if err != nil {
		return err
	}
	next, err := k.binding.UnpackNextPurchaseId(raw)
	if err != nil {
		return err
	}
	for ; k.next < next.Uint64(); k.next++ {
		k.open = append(k.open, k.next)
	}
	return nil
}

// settle reports whether an order remains open. Simulating the Vault call applies its round,
// pause, compliance and refund rules before submitting a transaction.
func (k *Keeper) settle(ctx context.Context, id uint64) (open bool, err error) {
	orderID := new(big.Int).SetUint64(id)
	data, err := k.binding.PackGetPurchase(orderID)
	if err != nil {
		return true, err
	}
	raw, err := blockchain.Call(ctx, k.client, k.vault, data)
	if err != nil {
		return true, fmt.Errorf("getPurchase: %w", err)
	}
	order, err := k.binding.UnpackGetPurchase(raw)
	if err != nil {
		return true, fmt.Errorf("getPurchase: %w", err)
	}
	if order.Status != orderPending {
		delete(k.sent, id)
		return false, nil
	}

	if busy, err := k.inFlight(ctx, id); busy || err != nil {
		return true, err
	}

	data, err = k.binding.PackSettlePurchase(orderID)
	if err != nil {
		return true, err
	}
	if _, err := blockchain.Call(ctx, k.client, k.vault, data); err != nil {
		if blockchain.IsRevert(err) {
			return true, nil // no usable round yet, paused or the buyer is blocked
		}
		return true, fmt.Errorf("simulate settlePurchase: %w", err)
	}
	// Leave the idempotency key unset so reverted attempts can be retried; the Vault rejects
	// duplicate settlement of a closed order, including after a reorg retry
	tx, err := k.txs.Submit(ctx, blockchain.SubmitRequest{
		Kind:            "settle_purchase",
		Signer:          k.signer,
		To:              k.vault,
		Data:            data,
		AllowReorgRetry: true,
	})
	if err != nil {
		return true, fmt.Errorf("submit settlePurchase: %w", err)
	}
	k.sent[id] = tx.ID
	log.Printf("keeper: order %d settlement sent in %s", id, tx.TxHash)
	return true, nil
}

// inFlight retains Pending, Mined and Confirmed transactions, attempting fee bumps for old
// Pending transactions. Other statuses clear the order's transaction link for another attempt.
func (k *Keeper) inFlight(ctx context.Context, id uint64) (bool, error) {
	txID, ok := k.sent[id]
	if !ok {
		return false, nil
	}
	tx, err := k.records.Get(ctx, txID)
	if err != nil {
		return true, err
	}
	switch tx.Status {
	case models.TxPending:
		if k.now().Sub(tx.SubmittedAt) > replaceAfter {
			replaced, err := k.txs.Replace(ctx, txID, k.signer, replaceBumpPercent)
			if err != nil {
				return true, fmt.Errorf("replace settlePurchase: %w", err)
			}
			k.sent[id] = replaced.ID
			log.Printf("keeper: order %d settlement replaced by %s", id, replaced.TxHash)
		}
		return true, nil
	case models.TxMined, models.TxConfirmed:
		return true, nil
	}
	delete(k.sent, id)
	return false, nil
}
