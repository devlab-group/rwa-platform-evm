package dto

// ErrorResponse mirrors components.schemas.Error in api/openapi.yaml. Built by
// the fail/failErr helpers in internal/api, which also own the stable
// machine-readable Code* values that go in Code.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}
