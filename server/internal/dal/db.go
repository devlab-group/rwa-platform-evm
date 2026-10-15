// Package dal groups persistence models, repository interfaces, and MongoDB/in-memory
// adapters under internal/dal. Connect supplies the shared MongoDB client constructor.
package dal

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Connect returns a client for the MongoDB at uri. Callers should defer
// client.Disconnect(ctx).
func Connect(ctx context.Context, uri string) (*mongo.Client, error) {
	return mongo.Connect(options.Client().ApplyURI(uri))
}
