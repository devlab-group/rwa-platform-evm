package api

import (
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

func TestSecurityStale(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	fresh := &models.SecurityState{AsOfBlock: 100, AsOfTime: now.Add(-10 * time.Second)}
	cp := &models.IndexerCheckpoint{LastBlock: 100, UpdatedAt: now.Add(-10 * time.Second)}

	cases := []struct {
		name string
		s    *models.SecurityState
		cp   *models.IndexerCheckpoint
		err  error
		want bool
	}{
		{"no projection yet", nil, cp, nil, true},
		{"checkpoint unreadable", fresh, nil, repository.ErrNotFound, true},
		{"reconciliation required", fresh, &models.IndexerCheckpoint{LastBlock: 100, ReconciliationRequired: true, UpdatedAt: now}, nil, true},
		{"projection time zero", &models.SecurityState{AsOfBlock: 100}, cp, nil, true},
		{"fresh projection", fresh, cp, nil, false},
		{"projection stalled beyond window", &models.SecurityState{AsOfBlock: 100, AsOfTime: now.Add(-5 * time.Minute)}, cp, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := securityStale(c.s, c.cp, c.err, now); got != c.want {
				t.Fatalf("securityStale = %v, want %v", got, c.want)
			}
		})
	}
}
