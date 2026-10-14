// Package alerts implements the operational alert evaluators. They are pure
// functions over domain state that the caller has already loaded, so they need
// no chain or database. cmd/platform runs them on a ticker and reports what
// they return.
package alerts

import (
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// Alert is one finding from an evaluator
type Alert struct {
	Kind         string // "pending_redemption_sla" or "funded_claim_failure"
	RedemptionID string
	Message      string
	Since        time.Time // when the condition being alerted on started
	Age          time.Duration
}

// EvaluatePendingRedemptionSLA returns one Alert per redemption request that
// has been Pending (requested on-chain, not yet funded) for at least threshold,
// measured from its on-chain creation time. A Pending redemption is not a
// payment guarantee, so an operator needs to know when one has waited that
// long.
func EvaluatePendingRedemptionSLA(requests []*models.RedemptionRequest, threshold time.Duration, now time.Time) []Alert {
	var out []Alert
	for _, r := range requests {
		if r.Status != models.RedemptionPending {
			continue
		}
		createdAt := time.Unix(r.CreatedAt, 0).UTC()
		age := now.Sub(createdAt)
		if age < threshold {
			continue
		}
		out = append(out, Alert{
			Kind: "pending_redemption_sla", RedemptionID: r.ID, Since: createdAt, Age: age,
			Message: fmt.Sprintf("redemption %s has been Pending for %s (SLA %s)", r.ID, age.Round(time.Second), threshold),
		})
	}
	return out
}

// EvaluateFundedClaimFailure returns one Alert per redemption request that has
// been Funded for at least threshold without reaching Completed. That means
// nobody has submitted the permissionless claimRedemption call, which points to
// a broken claim flow or a beneficiary who was never notified.
//
// The age is measured from FundedAt, the time the server indexed the
// RedemptionFunded event. Requests with no FundedAt are skipped.
func EvaluateFundedClaimFailure(requests []*models.RedemptionRequest, threshold time.Duration, now time.Time) []Alert {
	var out []Alert
	for _, r := range requests {
		if r.Status != models.RedemptionFunded {
			continue
		}
		if r.FundedAt.IsZero() {
			continue
		}
		fundedAt := r.FundedAt.UTC()
		age := now.Sub(fundedAt)
		if age < threshold {
			continue
		}
		out = append(out, Alert{
			Kind: "funded_claim_failure", RedemptionID: r.ID, Since: fundedAt, Age: age,
			Message: fmt.Sprintf("redemption %s has been Funded for %s without being claimed (SLA %s)", r.ID, age.Round(time.Second), threshold),
		})
	}
	return out
}
