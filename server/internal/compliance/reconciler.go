package compliance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// WebhookReconciler recovers Claiming events, submits Accepted decisions and tracks
// Applying transactions, marking Applied only after confirmation. ProcessDecision
// records deliveries without waiting for chain submission or confirmation, keeping
// relay delays out of HTTP responses.
// Reconcile is idempotent and runs at startup, then on a ticker in
// cmd/platform.startBackgroundLoops.
type WebhookReconciler struct {
	events repository.KYCEventRepository
	txs    repository.TransactionRepository
	status *StatusService
	guard  *Guard
	alert  AlertFunc
}

// SetGuard installs approval checks and an optional refusal alert. Cap/list refusals mark events
// Failed; read errors remain retryable.
func (r *WebhookReconciler) SetGuard(g *Guard, alert AlertFunc) {
	r.guard, r.alert = g, alert
}

// NewWebhookReconciler accepts nil status, leaving Accepted events unsubmitted; see
// submit
func NewWebhookReconciler(events repository.KYCEventRepository, txs repository.TransactionRepository, status *StatusService) *WebhookReconciler {
	return &WebhookReconciler{events: events, txs: txs, status: status}
}

// Reconcile advances Claiming/Accepted/Applying events, then reopens reorged Applied
// events. Per-event failures do not stop the batch; joined errors are returned.
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

// reopenReorgedApplied scans Applied events excluded by ListPending. A linked TxReorged
// resets the event to Accepted with TxID cleared for later resubmission through the
// supersede check.
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
	// Finalize interrupted Claiming events before checking supersession: they may not yet
	// hold their claim. finalizeClaim selects Accepted, Recorded or Superseded.
	if e.ApplyStatus == models.KYCApplyClaiming {
		_, err := finalizeClaim(ctx, r.events, e)
		return err
	}

	// A newer delivery may have claimed the address after acceptance. Supersede this event
	// before advancing it.
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
		return nil // Applied reorgs reopen separately
	}
}

// submit enqueues the on-chain status transaction for an Accepted event
func (r *WebhookReconciler) submit(ctx context.Context, e *models.KYCEvent) error {
	if r.status == nil {
		// Without a status service, keep the unrelayed decision Accepted rather than
		// reporting Applied
		return nil
	}
	statusVal, ok := StatusFromString(e.Outcome)
	if !ok {
		// ProcessDecision only accepts "Allowed" and "Blocked" for relay, so
		// this covers a malformed record
		e.ApplyStatus = models.KYCApplyFailed
		return r.events.Update(ctx, e)
	}
	if e.Outcome == "Allowed" && r.guard != nil {
		// Keep the local lock until the transaction is linked for subsequent cap checks
		r.guard.Lock()
		defer r.guard.Unlock()
		if err := r.guard.CheckAllow(ctx, e.Address, time.Now()); err != nil {
			kind := refusalAlert(err)
			if kind == "" {
				return err
			}
			e.ApplyStatus = models.KYCApplyFailed
			if uerr := r.events.Update(ctx, e); uerr != nil {
				return uerr
			}
			if r.alert != nil {
				r.alert(ctx, kind, e.Address, "approval from "+e.Provider+" recorded but not applied: "+err.Error())
			}
			return nil
		}
	}
	// Derive the key from (provider, eventId) so a crash after SetStatus but before Update
	// recovers through TxManager's existing idempotency record
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
		// Applying normally includes TxID. Reset a malformed record to Accepted instead of
		// waiting for a nonexistent transaction.
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
		// An on-chain revert requires operator attention; mark Failed without automatic retry
		e.ApplyStatus = models.KYCApplyFailed
		return r.events.Update(ctx, e)
	case models.TxFailed, models.TxReorged:
		// For TxFailed or TxReorged, clear TxID and return to Accepted for same-key
		// resubmission next tick. TxManager permits TxFailed retries; StatusService's
		// AllowReorgRetry enables TxReorged retries.
		e.ApplyStatus = models.KYCApplyAccepted
		e.TxID = ""
		return r.events.Update(ctx, e)
	default:
		// Other transaction states leave the event Applying
		return nil
	}
}
