package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
)

// projectDocID is the fixed _id of the project document. Using ProjectID as the _id
// would let two concurrent deploys with different ProjectIDs create two documents,
// because only application code enforced one project. With a fixed _id, the unique
// index on _id makes it a database guarantee.
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
