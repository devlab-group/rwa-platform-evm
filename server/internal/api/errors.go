package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// Stable machine-readable error codes
const (
	CodeBadRequest     = "bad_request"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodeUnauthorized   = "unauthorized"
	CodeInternal       = "internal_error"
	CodeNotImplemented = "not_configured"
	// CodeIdempotencyKeyRequired is returned by auth.Idempotency when a
	// side-effecting endpoint is called without an Idempotency-Key header.
	// auth.Idempotency uses the same literal.
	CodeIdempotencyKeyRequired = "idempotency_key_required"
	// CodeTooManyActiveChallenges is returned by createChallenge when an address
	// has too many active wallet challenges
	CodeTooManyActiveChallenges = "too_many_active_challenges"
)

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, dto.ErrorResponse{Code: code, Message: message})
}

// failInternal reports an unexpected server-side failure: err is logged in full
// with a short reference, and the client receives only that reference.
//
// Errors that reach a 500 come from the Mongo driver, the chain RPC client and
// the IPFS client, whose messages can carry host:port pairs, endpoint URLs and
// collection or index names. Several routes that can fail this way are public
// (project, config, sales/inventory, redemptions, transactions,
// compliance/allowed), so echoing err.Error() would expose the deployment's
// internals. A handler that needs to tell the caller something specific uses
// failErr with a 4xx.
func failInternal(c *gin.Context, err error) {
	// An oversized request body is a client error, so report it as 413
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		failErr(c, http.StatusRequestEntityTooLarge, "request_too_large", err)
		return
	}
	route := c.FullPath()
	if route == "" {
		route = c.Request.URL.Path
	}
	ref := errorReference()
	log.Printf("api: internal error [%s] %s %s: %v", ref, c.Request.Method, route, err)
	fail(c, http.StatusInternalServerError, CodeInternal, "internal error (reference "+ref+")")
}

// errorReference returns a short random tag that appears in both the log line
// and the response, so an operator can find the error behind a reported 500.
// The tag is not a secret and is not guaranteed unique. If the random read
// fails it returns the placeholder "unavailable" instead of failing the response.
func errorReference() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// failErr reports err at status and code. If err wraps the http.MaxBytesError
// raised by the reader that auth.MaxRequestBody installs, it reports 413
// "request_too_large" instead, so every handler enforces the body limit the
// same way.
func failErr(c *gin.Context, status int, code string, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(c, http.StatusRequestEntityTooLarge, "request_too_large", err.Error())
		return
	}
	fail(c, status, code, err.Error())
}

// notFoundOrInternal maps repository.ErrNotFound to 404 and anything else to 500
func notFoundOrInternal(c *gin.Context, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		fail(c, http.StatusNotFound, CodeNotFound, "resource not found")
		return
	}
	failInternal(c, err)
}
