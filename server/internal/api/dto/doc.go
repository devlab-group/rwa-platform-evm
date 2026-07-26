// Package dto holds the response view types the HTTP API serialises, plus the
// pure mappers that build them from internal/dal/models records. It exists so
// the handlers in internal/api stay HTTP-only: routing, auth, status codes and
// error mapping there, JSON shape here.
//
// The types mirror components.schemas.* in api/openapi.yaml (FROZEN,
// lead-owned) — field names and json tags are part of that contract, so
// changing one here is an API change, not a refactor.
//
// Mappers are deliberately dumb: they read a model (and whatever the caller
// already computed) and copy fields. Anything that needs a repository, the
// chain, or server configuration is computed by the handler and passed in as a
// parameter, so this package never grows a dependency beyond models.
package dto
