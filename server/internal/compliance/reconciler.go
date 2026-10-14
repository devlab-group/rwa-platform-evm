package compliance

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// WebhookReconciler is the outbox half of the webhook inbox/outbox flow. It
// submits the on-chain status transaction for an Accepted event, tracks it, and
// marks the event Applied only once the transaction is confirmed. ProcessDecision
// takes an event no further than Accepted or Recorded. The rest of the
// progression (Accepted, Applying, then Applied or Failed) happens here, so a
// slow or failing relay cannot block the HTTP webhook response.
//
// Reconcile is idempotent. It runs once at startup to recover events left
// Accepted or Applying, and then on a ticker (see startBackgroundLoops in
// cmd/platform).
type WebhookReconciler struct {
	events repository.KYCEventRepository
	txs    repository.TransactionRepository
	status *StatusService
}

// NewWebhookReconciler constructs a WebhookReconciler. status may be nil, in
// which case events stay Accepted (see submit)
func NewWebhookReconciler(events repository.KYCEventRepository, txs repository.TransactionRepository, status *StatusService) *WebhookReconciler {
	return &WebhookReconciler{events: events, txs: txs, status: status}
}

// Reconcile advances every Claiming, Accepted or Applying event by one step,
// then reopens any Applied event whose transaction has since been reorged out.
// A failure on one event does not stop the batch. The errors are joined and
// returned at the end.
func (r *WebhookReconciler) Reconcile(ctx context.Context) error {
	pending, err := r.events.ListPending(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range pending {
		if err := r.reconcileOne(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("kyc event %s (%s): %w", e.ID, e.Address, err))
		}
	}
	if err := r.reopenReorgedApplied(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// reopenReorgedApplied handles a reorg after confirmation. ListPending does not
// return Applied events, so one whose transaction RefreshStatuses later marks
// TxReorged would stay Applied without an on-chain effect. This resets such an
// event to Accepted with TxID cleared, and a later tick resubmits it through
// the normal path, including the supersede check.
func (r *WebhookReconciler) reopenReorgedApplied(ctx context.Context) error {
	all, err := r.events.ListAppliedWithTx(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range all {
		tx, err := r.txs.Get(ctx, e.TxID)
		if err != nil {
			errs = append(errs, fmt.Errorf("kyc event %s (%s): reopen check: %w", e.ID, e.Address, err))
			continue
		}
		if tx.Status != models.TxReorged {
			continue
		}
		e.ApplyStatus = models.KYCApplyAccepted
		e.TxID = ""
		if err := r.events.Update(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("kyc event %s (%s): reopen: %w", e.ID, e.Address, err))
		}
	}
	return errors.Join(errs...)
}

func (r *WebhookReconciler) reconcileOne(ctx context.Context, e *models.KYCEvent) error {
	// A Claiming event was stored but its request died before the claim was
	// resolved. finalizeClaim makes it Accepted or Recorded if it holds the
	// per-address claim, and Superseded otherwise. This must run before the
	// supersede check below, which would wrongly supersede an event whose
	// claim has not advanced yet.
	if e.ApplyStatus == models.KYCApplyClaiming {
		_, err := finalizeClaim(ctx, r.events, e)
		return err
	}

	// Do not advance a decision the claim has moved past. A newer delivery for
	// the address may have won ClaimLatestForAddress after this event was
	// accepted, and a stale decision must not be applied out of order.
	currentKey, err := r.events.CurrentClaimEventKey(ctx, e.Address)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if err == nil && currentKey != kycEventIDKey(e.Provider, e.EventID) {
		e.ApplyStatus = models.KYCApplySuperseded
		return r.events.Update(ctx, e)
	}

	switch e.ApplyStatus {
	case models.KYCApplyAccepted:
		return r.submit(ctx, e)
	case models.KYCApplyApplying:
		return r.checkSubmitted(ctx, e)
	default:
		return nil // Applied, Superseded, Failed and Recorded are terminal
	}
}

// submit enqueues the on-chain status transaction for an Accepted event
func (r *WebhookReconciler) submit(ctx context.Context, e *models.KYCEvent) error {
	if r.status == nil {
		// Without a compliance hot key the event is stored but cannot be
		// applied on-chain. It stays Accepted rather than being reported as
		// Applied for a decision nothing relayed
		return nil
	}
	statusVal, ok := StatusFromString(e.Outcome)
	if !ok {
		// ProcessDecision only accepts "Allowed" and "Blocked" for relay, so
		// this covers a malformed record
		e.ApplyStatus = models.KYCApplyFailed
		return r.events.Update(ctx, e)
	}
	// The key derives from (provider, eventId), so if the process crashes after
	// SetStatus succeeds and before the Update below, the next tick resubmits
	// under the same TxManager idempotency key and gets the existing
	// transaction instead of creating a second one
	idemKey := "kyc-status:" + kycEventIDKey(e.Provider, e.EventID)
	tx, err := r.status.SetStatus(ctx, idemKey, common.HexToAddress(e.Address), statusVal, uint64(e.ValidUntil))
	if err != nil {
		return err // ApplyStatus stays Accepted; retried next tick
	}
	e.TxID = tx.ID
	e.ApplyStatus = models.KYCApplyApplying
	return r.events.Update(ctx, e)
}

// checkSubmitted advances an Applying event once its linked transaction
// resolves
func (r *WebhookReconciler) checkSubmitted(ctx context.Context, e *models.KYCEvent) error {
	if e.TxID == "" {
		// submit sets TxID together with Applying, so this is not expected.
		// Reset to Accepted so the event does not wait on a transaction that
		// does not exist
		e.ApplyStatus = models.KYCApplyAccepted
		return r.events.Update(ctx, e)
	}
	tx, err := r.txs.Get(ctx, e.TxID)
	if err != nil {
		return err
	}
	switch tx.Status {
	case models.TxConfirmed:
		e.ApplyStatus = models.KYCApplyApplied
		return r.events.Update(ctx, e)
	case models.TxReverted:
		// The transaction was mined and reverted on-chain. It needs operator
		// attention, so it is terminal and not retried automatically
		e.ApplyStatus = models.KYCApplyFailed
		return r.events.Update(ctx, e)
	case models.TxFailed, models.TxReorged:
		// TxFailed means the send errored and nothing reached the mempool.
		// TxReorged means the transaction was mined but its block is no longer
		// canonical. Either way nothing durable happened, so clear TxID and go
		// back to Accepted. The next tick resubmits under the same idempotency
		// key, which TxManager accepts for TxFailed records and, because
		// StatusService sets AllowReorgRetry, for TxReorged ones.
		e.ApplyStatus = models.KYCApplyAccepted
		e.TxID = ""
		return r.events.Update(ctx, e)
	default:
		// Pending, Mined and Replaced are in flight or tracked through a
		// replacement transaction, so leave the event Applying
		return nil
	}
}
