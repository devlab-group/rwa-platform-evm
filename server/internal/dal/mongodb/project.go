package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
)

// projectDocID fixes MongoDB's project _id so its unique index enforces one document
// even when concurrent deployments have different ProjectIDs
const projectDocID = "singleton"

// projectRepo stores the single project document, one per deployment, under
// projectDocID
type projectRepo struct{ coll *mongo.Collection }

func (r *projectRepo) Get(ctx context.Context) (*models.Project, error) {
	return getByID[models.Project](ctx, r.coll, projectDocID)
}

func (r *projectRepo) Upsert(ctx context.Context, p *models.Project) error {
	return upsertByID(ctx, r.coll, projectDocID, p)
}
