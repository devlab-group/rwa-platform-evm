package models

import "time"

// IdempotencyRecord stores the response of a state-changing request, so a repeat
// with the same Idempotency-Key returns that response and repeats no side effect
type IdempotencyRecord struct {
	Key         string `json:"key" bson:"_id"`
	Method      string `json:"method" bson:"method"`
	Path        string `json:"path" bson:"path"`
	RequestHash string `json:"requestHash" bson:"requestHash"`
	// Token is the fencing token that Reserve issues to the caller that owns the
	// reservation. Complete and Release take effect only when the caller's token equals
	// it, so a slow request whose reservation expired and was taken over by a later
	// Reserve cannot overwrite the new owner's record (see IdempotencyRepository).
	Token          string    `json:"-" bson:"token"`
	ResponseStatus int       `json:"responseStatus" bson:"responseStatus"`
	ResponseBody   []byte    `json:"responseBody" bson:"responseBody"`
	CreatedAt      time.Time `json:"createdAt" bson:"createdAt"`
	ExpiresAt      time.Time `json:"expiresAt" bson:"expiresAt"`
}
