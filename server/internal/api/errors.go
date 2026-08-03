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

// failInternal reports an unexpected server-side failure: err is logged in full
// with a short reference, and the client is told only that reference.
//
// Errors reaching a 500 here come from the Mongo driver, the chain RPC client
// and the IPFS client, whose messages carry host:port pairs, endpoint URLs, and
// collection/index names. Several routes that can hit one are deliberately
// public and unauthenticated (project, config, sales/inventory, redemptions,
// transactions, compliance/allowed), so echoing err.Error() would let anyone
// map the deployment's internals just by polling a read endpoint while a
// backend is unhealthy. Handlers that want to tell the caller something
// specific should keep using failErr with a 4xx.
func failInternal(c *gin.Context, err error) {
	// An oversized body is the caller's problem, not ours — let failErr keep
	// mapping it to 413 rather than burying it as an opaque 500.
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

// errorReference is a short random tag that appears in both the log line and
// the response, so an operator can find the real error for a 500 a user
// reports. Not a secret and not unique forever — it only has to be findable in
// the log, so 4 bytes is plenty. A failed read degrades to a fixed placeholder
// rather than failing the response we are already in the middle of writing.
func errorReference() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
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
	failInternal(c, err)
}
