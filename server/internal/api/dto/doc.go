// Package dto holds the response view types the HTTP API serializes and the
// pure mappers that build them from internal/dal/models records. Handlers in
// internal/api own routing, auth, status codes and error mapping; this package
// owns the JSON shape.
//
// The types mirror components.schemas.* in api/openapi.yaml, so a field name or
// json tag change here changes the API.
//
// Mappers read a model and any values the caller computed, and copy fields.
// Anything that needs a repository, the chain or server configuration is
// computed by the handler and passed in, so this package depends only on
// models.
package dto
