package mongodb

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
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

type assetProfileRepo struct{ coll *mongo.Collection }

func (r *assetProfileRepo) Get(ctx context.Context, projectID string) (*models.AssetProfile, error) {
	return getByID[models.AssetProfile](ctx, r.coll, projectID)
}

// GetCurrent returns the single current profile (most recent by createdAt),
// or repository.ErrNotFound when none exists — see the interface doc comment.
func (r *assetProfileRepo) GetCurrent(ctx context.Context) (*models.AssetProfile, error) {
	return findOne[models.AssetProfile](ctx, r.coll, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
}

func (r *assetProfileRepo) Upsert(ctx context.Context, p *models.AssetProfile) error {
	return upsertByID(ctx, r.coll, p.ProjectID, p)
}

// Create is create-once/CAS via InsertOne (never upsert): Mongo's default
// unique index on _id (== ProjectID here) makes a second concurrent create
// for the same projectId fail atomically at the database (see
// the interface doc comment; same pattern as idempotencyRepo.Reserve).
func (r *assetProfileRepo) Create(ctx context.Context, p *models.AssetProfile) error {
	return createDoc(ctx, r.coll, p)
}

type assetRecordRepo struct{ coll *mongo.Collection }

func (r *assetRecordRepo) List(ctx context.Context) ([]*models.AssetRecord, error) {
	return findMany[models.AssetRecord](ctx, r.coll, bson.M{}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
}

// ListPage returns one bounded, repository-level keyset page — see
// the interface doc comment. Sorted (createdAt asc, _id asc), matching
// List's pre-existing order.
func (r *assetRecordRepo) ListPage(ctx context.Context, cursor string, limit int) ([]*models.AssetRecord, string, error) {
	items, err := findMany[models.AssetRecord](ctx, r.coll, keysetFilterDirDate(cursor, "createdAt", false),
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit+1)))
	if err != nil {
		return nil, "", err
	}
	return trimKeysetPage(items, limit, func(r *models.AssetRecord) (int64, string) { return r.CreatedAt.UnixNano(), r.RecordID })
}

func (r *assetRecordRepo) Get(ctx context.Context, recordID string) (*models.AssetRecord, error) {
	return getByID[models.AssetRecord](ctx, r.coll, recordID)
}

func (r *assetRecordRepo) Create(ctx context.Context, rec *models.AssetRecord) error {
	return createDoc(ctx, r.coll, rec)
}

func (r *assetRecordRepo) Update(ctx context.Context, rec *models.AssetRecord) error {
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": rec.RecordID}, rec)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// UpdateConditional is the storage-level CAS (mirror of
// transactionRepo.UpdateConditional): the filter's version match/no-match IS
// the atomicity. MatchedCount 0 means either the id is gone or a concurrent
// writer advanced version, distinguished by a follow-up Get.
func (r *assetRecordRepo) UpdateConditional(ctx context.Context, rec *models.AssetRecord, expectedVersion int) (bool, error) {
	cp := *rec
	cp.Version = expectedVersion + 1
	res, err := r.coll.ReplaceOne(ctx, bson.M{"_id": rec.RecordID, "version": expectedVersion}, cp)
	if err != nil {
		return false, err
	}
	if res.MatchedCount > 0 {
		return true, nil
	}
	if _, err := r.Get(ctx, rec.RecordID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, repository.ErrNotFound
		}
		return false, err
	}
	return false, nil
}

type auditPackageRepo struct{ coll *mongo.Collection }

func (r *auditPackageRepo) Get(ctx context.Context, recordID string) (*models.AuditPackage, error) {
	return getByID[models.AuditPackage](ctx, r.coll, recordID)
}

func (r *auditPackageRepo) Upsert(ctx context.Context, p *models.AuditPackage) error {
	return upsertByID(ctx, r.coll, p.RecordID, p)
}

type attestationRepo struct{ coll *mongo.Collection }

func (r *attestationRepo) Get(ctx context.Context, recordID string) (*models.Attestation, error) {
	return getByID[models.Attestation](ctx, r.coll, recordID)
}

func (r *attestationRepo) Upsert(ctx context.Context, a *models.Attestation) error {
	return upsertByID(ctx, r.coll, a.RecordID, a)
}
