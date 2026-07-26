package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Stable machine-readable error codes.
const (
	CodeBadRequest     = "bad_request"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodeUnauthorized   = "unauthorized"
	CodeInternal       = "internal_error"
	CodeNotImplemented = "not_configured"
	// CodeIdempotencyKeyRequired is returned by auth.Idempotency when a
	// side-effecting endpoint is called with no Idempotency-Key header.
	// Declared alongside the other stable codes here (rather than in
	// internal/auth) so every consumer-facing error
	// code lives in one place; auth.Idempotency uses the identical literal.
	CodeIdempotencyKeyRequired = "idempotency_key_required"
	// CodeTooManyActiveChallenges is returned by createChallenge once an
	// address has too many currently-active wallet challenges outstanding.
	CodeTooManyActiveChallenges = "too_many_active_challenges"
)

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, dto.ErrorResponse{Code: code, Message: message})
}

// failErr reports err at status/code, EXCEPT when err (however it got
// wrapped by gin's JSON binding or a handler's own io.ReadAll) traces back
// to the http.MaxBytesReader installed by auth.MaxRequestBody: that always
// reports 413, regardless of what status the specific call site asked for,
// so every handler gets the request-size limit enforced the same way
// without each one needing its own detection code.
func failErr(c *gin.Context, status int, code string, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(c, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	fail(c, status, code, err.Error())
}

// notFoundOrInternal maps repository.ErrNotFound to 404 and anything else to 500.
func notFoundOrInternal(c *gin.Context, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		fail(c, http.StatusNotFound, CodeNotFound, "resource not found")
		return
	}
	failErr(c, http.StatusInternalServerError, CodeInternal, err)
}
