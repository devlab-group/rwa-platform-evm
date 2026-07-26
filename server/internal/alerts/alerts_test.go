package alerts

import (
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

func TestEvaluatePendingRedemptionSLA(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	threshold := 48 * time.Hour

	requests := []*models.RedemptionRequest{
		{ID: "old-pending", Status: models.RedemptionPending, CreatedAt: now.Add(-72 * time.Hour).Unix()},
		{ID: "fresh-pending", Status: models.RedemptionPending, CreatedAt: now.Add(-1 * time.Hour).Unix()},
		{ID: "exactly-at-threshold", Status: models.RedemptionPending, CreatedAt: now.Add(-threshold).Unix()},
		{ID: "old-but-funded", Status: models.RedemptionFunded, CreatedAt: now.Add(-72 * time.Hour).Unix()},
		{ID: "old-but-completed", Status: models.RedemptionCompleted, CreatedAt: now.Add(-72 * time.Hour).Unix()},
	}

	got := EvaluatePendingRedemptionSLA(requests, threshold, now)

	// old-pending (72h) and exactly-at-threshold (48h == threshold, and the
	// evaluator alerts AT the SLA boundary, not just strictly past it) both
	// fire; fresh-pending, and the two non-Pending records regardless of
	// age, do not.
	if len(got) != 2 {
		t.Fatalf("got %d alerts, want 2: %+v", len(got), got)
	}
	ids := map[string]bool{got[0].RedemptionID: true, got[1].RedemptionID: true}
	if !ids["old-pending"] || !ids["exactly-at-threshold"] {
		t.Errorf("alerts = %+v, want old-pending and exactly-at-threshold", got)
	}
	for _, a := range got {
		if a.Kind != "pending_redemption_sla" {
			t.Errorf("Kind = %q", a.Kind)
		}
	}
}

func TestEvaluateFundedClaimFailure(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	threshold := 24 * time.Hour

	requests := []*models.RedemptionRequest{
		{ID: "stuck-funded", Status: models.RedemptionFunded, UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "recently-funded", Status: models.RedemptionFunded, UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: "old-but-completed", Status: models.RedemptionCompleted, UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "old-but-pending", Status: models.RedemptionPending, UpdatedAt: now.Add(-48 * time.Hour)},
	}

	got := EvaluateFundedClaimFailure(requests, threshold, now)

	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1: %+v", len(got), got)
	}
	if got[0].RedemptionID != "stuck-funded" {
		t.Errorf("alert = %+v, want stuck-funded", got[0])
	}
	if got[0].Kind != "funded_claim_failure" {
		t.Errorf("Kind = %q", got[0].Kind)
	}
}

func TestEvaluatorsReturnNilForNoFindings(t *testing.T) {
	now := time.Now()
	requests := []*models.RedemptionRequest{
		{ID: "fine", Status: models.RedemptionPending, CreatedAt: now.Unix()},
	}
	if got := EvaluatePendingRedemptionSLA(requests, 48*time.Hour, now); len(got) != 0 {
		t.Errorf("got %d alerts, want 0: %+v", len(got), got)
	}
	if got := EvaluateFundedClaimFailure(requests, 24*time.Hour, now); len(got) != 0 {
		t.Errorf("got %d alerts, want 0: %+v", len(got), got)
	}
}

func TestEvaluatorsHandleEmptyInput(t *testing.T) {
	now := time.Now()
	if got := EvaluatePendingRedemptionSLA(nil, time.Hour, now); len(got) != 0 {
		t.Errorf("got %d alerts for nil input, want 0", len(got))
	}
	if got := EvaluateFundedClaimFailure(nil, time.Hour, now); len(got) != 0 {
		t.Errorf("got %d alerts for nil input, want 0", len(got))
	}
}
