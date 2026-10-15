// Package alerts evaluates caller-loaded domain state without chain or database access.
// cmd/platform runs the evaluators on a ticker and reports their alerts.
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

// EvaluatePendingRedemptionSLA alerts for each Pending redemption whose on-chain
// creation time is at least threshold old. Pending means requested but unfunded, with
// no payment guarantee; the alert flags requests needing operator attention.
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

// EvaluateFundedClaimFailure alerts for each redemption still Funded at least threshold
// after FundedAt, when the server indexed RedemptionFunded. Requests without FundedAt
// are skipped. A delayed completion may indicate a broken permissionless
// claimRedemption flow or a missing beneficiary notification; it does not establish
// whether anyone submitted a claim.
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
