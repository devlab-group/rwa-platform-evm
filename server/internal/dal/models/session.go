package models

import "time"

// WalletSession stores investor sessions in wallet_sessions, shared across server
// replicas using MongoDB or local to a memory process. auth.SessionManager stores and
// looks up SHA-256 digests; raw bearer tokens are returned only at issuance, keeping
// collection access from yielding usable credentials.
type WalletSession struct {
	Token     string    `json:"-" bson:"_id"`
	Address   string    `json:"address" bson:"address"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
}
