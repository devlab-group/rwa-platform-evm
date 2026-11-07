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
	// orderNone and orderPending are the Vault's PurchaseStatus.None and Pending
	orderNone    = 0
	orderPending = 1
)

// ErrNotVault means nextPurchaseId reverted or could not be decoded at the configured address
var ErrNotVault = errors.New("keeper: vault_address does not answer nextPurchaseId, so it is not an rwa-v4 Vault")

// Fatal identifies passes that require operator attention: an invalid Vault response or
// a transaction blocking the wallet. Resolve a stuck nonce, then restart to clear the
// keeper's in-memory transaction record.
func Fatal(err error) bool {
	return errors.Is(err, ErrNotVault) || errors.Is(err, blockchain.ErrNeedsIntervention)
}

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
	open []uint64          // seen ids that may still settle, oldest first, including ids not read yet
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
// Check them with Fatal to see whether trying again can help.
func (k *Keeper) Pass(ctx context.Context) error {
	var errs []error
	if err := k.txs.RefreshStatuses(ctx, confirmations); err != nil {
		errs = append(errs, fmt.Errorf("refresh transactions: %w", err))
	}
	if err := k.discover(ctx); err != nil {
		errs = append(errs, err)
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

// Run calls Pass every interval until ctx ends, logging failures. It returns early with the
// error when a pass fails for a reason retrying cannot fix (see Fatal).
func (k *Keeper) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := k.Pass(ctx); err != nil && ctx.Err() == nil {
				if Fatal(err) {
					return err
				}
				log.Printf("keeper: %v", err)
			}
		}
	}
}

// discover queues the ids from k.next up to nextPurchaseId. Reading an order is left to settle,
// which keeps an unread id in k.open until it reads, so a failed pass loses nothing. A shorter
// nextPurchaseId than before means a reorg removed the newest requests: the ids above it are
// dropped, and a different order that takes one of them is discovered again.
func (k *Keeper) discover(ctx context.Context) error {
	data, err := k.binding.PackNextPurchaseId()
	if err != nil {
		return err
	}
	raw, err := blockchain.Call(ctx, k.client, k.vault, data)
	if err != nil {
		if blockchain.IsRevert(err) {
			return fmt.Errorf("%w: %v", ErrNotVault, err)
		}
		return fmt.Errorf("read nextPurchaseId: %w", err)
	}
	next, err := k.binding.UnpackNextPurchaseId(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotVault, err)
	}
	if end := next.Uint64(); end < k.next {
		k.next = end
		kept := k.open[:0]
		for _, id := range k.open {
			if id < end {
				kept = append(kept, id)
			}
		}
		k.open = kept
	}
	// A cold start reads every order from id 0 with a separate eth_call. Persisting the
	// cursor or choosing a recent starting id would reduce work for long histories.
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
	if order.Status == orderNone {
		// not a closed order: the node answering this read is behind the one that gave
		// nextPurchaseId, or a reorg removed the request. discover drops the id if
		// nextPurchaseId shrank; otherwise read it again next pass
		return true, nil
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
		// An ambiguous broadcast leaves a record that holds the nonce and may still land; track
		// it so the next pass does not queue a second settlement behind it
		if held := k.unknownBroadcast(ctx, data); held != "" {
			k.sent[id] = held
		}
		return true, fmt.Errorf("submit settlePurchase: %w", err)
	}
	k.sent[id] = tx.ID
	log.Printf("keeper: order %d settlement sent in %s", id, tx.TxHash)
	return true, nil
}

// unknownBroadcast returns the record Submit left in TxBroadcastUnknown for these call data,
// or "" when there is none
func (k *Keeper) unknownBroadcast(ctx context.Context, data []byte) string {
	unknown, err := k.records.ListByStatuses(ctx, models.TxBroadcastUnknown)
	if err != nil {
		return ""
	}
	want := "0x" + common.Bytes2Hex(data)
	for _, tx := range unknown {
		if tx.Data == want {
			return tx.ID
		}
	}
	return ""
}

// inFlight retains BroadcastUnknown, Pending, Mined and Confirmed transactions, attempting fee
// bumps for old Pending transactions. A Replaced record leads to its replacement. Other statuses
// clear the order's transaction link for another attempt.
func (k *Keeper) inFlight(ctx context.Context, id uint64) (bool, error) {
	txID, ok := k.sent[id]
	if !ok {
		return false, nil
	}
	tx, err := k.records.Get(ctx, txID)
	if err != nil {
		return true, err
	}
	if tx.Status == models.TxReplaced && tx.ReplacedBy != "" {
		// a replacement whose broadcast outcome was unknown replaced the record we tracked
		k.sent[id] = tx.ReplacedBy
		return true, nil
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
	case models.TxBroadcastUnknown, models.TxMined, models.TxConfirmed:
		return true, nil
	}
	delete(k.sent, id)
	return false, nil
}
