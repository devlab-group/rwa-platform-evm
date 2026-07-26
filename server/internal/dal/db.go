// Package dal is the data-access layer. It groups the persistence-agnostic
// models (internal/dal/models), the storage interfaces
// (internal/dal/repository), and their MongoDB and in-memory implementations
// (internal/dal/mongodb, internal/dal/memory). db.go holds the shared Mongo
// connection bootstrap every entrypoint dials through.
package dal

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Connect dials MongoDB at uri and returns a ready client. Callers should
// defer client.Disconnect(ctx).
func Connect(ctx context.Context, uri string) (*mongo.Client, error) {
	return mongo.Connect(options.Client().ApplyURI(uri))
}
