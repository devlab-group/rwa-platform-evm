// Package models holds the persistence-agnostic data model shared by
// internal/dal/repository, internal/api and the workflow packages. Its types mirror
// the persisted collections and the schemas in api/openapi.yaml. On-chain uint256
// amounts are decimal strings, never JSON numbers, so they keep their precision.
package models
