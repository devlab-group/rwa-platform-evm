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
	// CodeHolderCapReached reports a new-wallet approval refused by the configured cap
	CodeHolderCapReached = "holder_cap_reached"
	// CodeSanctionedAddress reports an Allowed submission refused by sanctions screening
	CodeSanctionedAddress = "sanctioned_address"
)

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, dto.ErrorResponse{Code: code, Message: message})
}

// failInternal logs the full error and returns only a short reference, keeping Mongo,
// RPC and IPFS hosts, URLs, collection and index names private. Use failErr with 4xx
// for safe details.
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

// errorReference generates a short random tag linking a client-reported 500 to its log
// entry. It is neither secret nor guaranteed unique. Failed randomness returns
// "unavailable" without failing the response.
func errorReference() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// failErr returns status and code, except wrapped http.MaxBytesError from
// auth.MaxRequestBody becomes 413 "request_too_large" for consistent body-limit
// handling
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
