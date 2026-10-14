package dto

// ErrorResponse mirrors components.schemas.Error in api/openapi.yaml. The
// fail and failErr helpers in package api build it and own the stable Code*
// values.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}
