package main

import (
	"context"
	"errors"
	"testing"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// failingProjects fails every read, as a dropped database connection would
type failingProjects struct{ repository.ProjectRepository }

var errProjectRead = errors.New("connection reset")

func (failingProjects) Get(context.Context) (*models.Project, error) { return nil, errProjectRead }

func TestPurchaseTimeoutSeconds(t *testing.T) {
	ctx := context.Background()

	repos := memory.New()
	if err := repos.Projects.Upsert(ctx, &models.Project{PurchaseTimeout: 86400}); err != nil {
		t.Fatal(err)
	}
	if got, err := purchaseTimeoutSeconds(ctx, repos); err != nil || got != 86400 {
		t.Errorf("purchaseTimeoutSeconds = %d, %v; want 86400, nil", got, err)
	}

	// an earlier project has no purchase timeout, which is not an error
	repos = memory.New()
	if err := repos.Projects.Upsert(ctx, &models.Project{}); err != nil {
		t.Fatal(err)
	}
	if got, err := purchaseTimeoutSeconds(ctx, repos); err != nil || got != 0 {
		t.Errorf("earlier project: purchaseTimeoutSeconds = %d, %v; want 0, nil", got, err)
	}

	// an unreadable project must surface, not read as a timeout of 0
	repos = memory.New()
	repos.Projects = failingProjects{}
	if _, err := purchaseTimeoutSeconds(ctx, repos); !errors.Is(err, errProjectRead) {
		t.Errorf("unreadable project: err = %v, want the read failure", err)
	}
}
