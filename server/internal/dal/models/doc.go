// Package models holds the persistence-agnostic data model shared by
// internal/dal/repository, internal/api, and the workflow packages. Types here
// mirror the persisted collections and the api/openapi.yaml schemas.
// Amounts that are on-chain uint256 values are always decimal strings, never
// JSON numbers, to avoid precision loss.
package models
