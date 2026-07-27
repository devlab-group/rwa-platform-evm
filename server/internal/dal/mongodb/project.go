package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/rwa-platform/server/internal/dal/models"
)

// projectDocID is the fixed technical _id every project document uses.
// Using p.ProjectID as _id would let two concurrent deploys with two
// different ProjectIDs each land as a
// SEPARATE document, since nothing but application code enforced "one
// project total" — Get's bson.M{} filter just grabbed whichever one
// happened to match first. A fixed _id makes "one project total" a real
// uniqueness guarantee at the database (every Mongo collection has an
// automatic unique index on _id), not just a convention.
const projectDocID = "singleton"

// projectRepo stores the single project document (one project per
// deployment) under the fixed projectDocID.
type projectRepo struct{ coll *mongo.Collection }

func (r *projectRepo) Get(ctx context.Context) (*models.Project, error) {
	return getByID[models.Project](ctx, r.coll, projectDocID)
}

func (r *projectRepo) Upsert(ctx context.Context, p *models.Project) error {
	return upsertByID(ctx, r.coll, projectDocID, p)
}
