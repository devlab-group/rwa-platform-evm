// Package alerts implements the operational alert evaluators: pure
// functions over already-loaded domain state, deliberately with no I/O of
// their own, so they're testable against fake/in-memory data without a
// live chain or Mongo (cmd/platform wires a periodic ticker that loads
// state, calls these, and reports whatever they return — see main.go).
package alerts

import (
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// Alert is one SLA/health-check finding an evaluator produced.
type Alert struct {
	Kind         string // "pending_redemption_sla" | "funded_claim_failure"
	RedemptionID string
	Message      string
	Since        time.Time // when the condition being alerted on started
	Age          time.Duration
}

// EvaluatePendingRedemptionSLA returns one Alert per redemption request
// that has been sitting in Pending (requested on-chain, not yet funded)
// longer than threshold. A Pending redemption is not a payment guarantee,
// which is exactly why an operator needs to know when one has sat Pending
// long enough that the beneficiary is likely wondering where their funds
// are.
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

// EvaluateFundedClaimFailure returns one Alert per redemption request that
// has been Funded (the treasurer paid in) longer than threshold without
// transitioning to Completed — i.e. nobody has successfully submitted the
// permissionless claimRedemption call, which usually means either the claim
// flow is broken somewhere, or funds are sitting unclaimed because the
// beneficiary was never notified.
//
// UpdatedAt is used as an approximation of "became Funded at": there is no
// dedicated FundedAt timestamp field (only FundedAtBlock, a block number,
// not wall-clock time — see models.RedemptionRequest's doc comment), but
// redemption.Reconcile only writes UpdatedAt when this record's derived
// state actually changes in response to a new chain event, so for a
// redemption that reached Funded and has sat there untouched since,
// UpdatedAt closely tracks the real Funded transition time.
func EvaluateFundedClaimFailure(requests []*models.RedemptionRequest, threshold time.Duration, now time.Time) []Alert {
	var out []Alert
	for _, r := range requests {
		if r.Status != models.RedemptionFunded {
			continue
		}
		fundedAt := r.UpdatedAt.UTC()
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
