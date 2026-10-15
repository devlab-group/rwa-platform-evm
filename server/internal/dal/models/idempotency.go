package models

import "time"

// IdempotencyRecord stores a write response for replay without repeating its effect
// while the reservation remains live
type IdempotencyRecord struct {
	Key         string `json:"key" bson:"_id"`
	Method      string `json:"method" bson:"method"`
	Path        string `json:"path" bson:"path"`
	RequestHash string `json:"requestHash" bson:"requestHash"`
	// Token identifies Reserve's winning reservation. Complete and Release require this
	// token, preventing an expired, superseded owner from modifying a later reservation;
	// see IdempotencyRepository.
	Token          string    `json:"-" bson:"token"`
	ResponseStatus int       `json:"responseStatus" bson:"responseStatus"`
	ResponseBody   []byte    `json:"responseBody" bson:"responseBody"`
	CreatedAt      time.Time `json:"createdAt" bson:"createdAt"`
	ExpiresAt      time.Time `json:"expiresAt" bson:"expiresAt"`
}
