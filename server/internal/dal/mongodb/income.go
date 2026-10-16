package mongodb

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

type incomePreparationRepo struct{ coll *mongo.Collection }

func (r *incomePreparationRepo) Create(ctx context.Context, p *models.IncomePreparation) error {
	return createDoc(ctx, r.coll, p)
}

func (r *incomePreparationRepo) Get(ctx context.Context, id string) (*models.IncomePreparation, error) {
	return getByID[models.IncomePreparation](ctx, r.coll, id)
}

func (r *incomePreparationRepo) SetClaimDeadline(ctx context.Context, id string, deadline int64) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"claimDeadline": deadline}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// List sorts oldest first, by (createdAt, _id), without snapshotRaw
func (r *incomePreparationRepo) List(ctx context.Context) ([]*models.IncomePreparation, error) {
	return findMany[models.IncomePreparation](ctx, r.coll, bson.M{},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).
			SetProjection(bson.M{"snapshotRaw": 0}))
}

type incomeDistributionRepo struct{ coll *mongo.Collection }

func (r *incomeDistributionRepo) Upsert(ctx context.Context, d *models.IncomeDistribution) error {
	return upsertByID(ctx, r.coll, d.ID, d)
}

// List sorts in creation order, by (createdBlock, createdLogIndex)
func (r *incomeDistributionRepo) List(ctx context.Context) ([]*models.IncomeDistribution, error) {
	return findMany[models.IncomeDistribution](ctx, r.coll, bson.M{},
		options.Find().SetSort(bson.D{{Key: "createdBlock", Value: 1}, {Key: "createdLogIndex", Value: 1}}))
}

func (r *incomeDistributionRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"generation": bson.M{"$ne": gen}})
	return err
}

type incomeClaimRepo struct{ coll *mongo.Collection }

func (r *incomeClaimRepo) Upsert(ctx context.Context, c *models.IncomeClaim) error {
	return upsertByID(ctx, r.coll, c.ID, c)
}

// ListByAccount matches the lowercase account that income.BuildDistributions stores, so the
// account index serves it
func (r *incomeClaimRepo) ListByAccount(ctx context.Context, account string) ([]*models.IncomeClaim, error) {
	return findMany[models.IncomeClaim](ctx, r.coll, bson.M{"account": strings.ToLower(account)},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
}

func (r *incomeClaimRepo) DeleteStaleGeneration(ctx context.Context, gen int64) error {
	_, err := r.coll.DeleteMany(ctx, bson.M{"generation": bson.M{"$ne": gen}})
	return err
}
