package models

import "time"

// IdempotencyRecord stores a completed state-changing request's response so
// a repeated request with the same Idempotency-Key returns the identical
// result instead of re-executing a side effect.
type IdempotencyRecord struct {
	Key         string `json:"key" bson:"_id"`
	Method      string `json:"method" bson:"method"`
	Path        string `json:"path" bson:"path"`
	RequestHash string `json:"requestHash" bson:"requestHash"`
	// Token is the fencing/lease token minted by Reserve for whichever
	// caller currently owns this reservation. Without a fencing token, an
	// expired reservation could be taken over while an old, slow request
	// still completes it — clobbering the new owner. Complete and
	// Release both require the caller's token to match the record's current
	// Token before they take effect — see IdempotencyRepository's doc
	// comment — so a request that was slow enough for its reservation to
	// expire and be taken over by a fresh Reserve can no longer clobber the
	// new owner's in-flight/completed record with its own stale write.
	Token          string    `json:"-" bson:"token"`
	ResponseStatus int       `json:"responseStatus" bson:"responseStatus"`
	ResponseBody   []byte    `json:"responseBody" bson:"responseBody"`
	CreatedAt      time.Time `json:"createdAt" bson:"createdAt"`
	ExpiresAt      time.Time `json:"expiresAt" bson:"expiresAt"`
}
