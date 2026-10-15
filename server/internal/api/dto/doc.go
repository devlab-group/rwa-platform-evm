// Package dto owns API response JSON shapes matching components.schemas.* in
// api/openapi.yaml; changing fields or json tags changes the contract. Pure mappers
// copy models and caller-computed values. Handlers in internal/api own routing, auth,
// status/error mapping, repositories, chain access and configuration.
package dto
