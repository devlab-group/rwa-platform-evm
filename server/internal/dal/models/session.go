package models

import "time"

// WalletSession is an investor's short-lived session (collection:
// wallet_sessions). Sessions live in the shared store and not in a process-local
// map, so a token issued by one server replica validates on every replica sharing
// the store and a logout on any replica revokes it everywhere.
//
// Token holds the SHA-256 digest of the opaque bearer token, not the token. The
// caller receives the raw token once at issue and it is never stored, so read access
// to this collection does not yield a usable credential. auth.SessionManager derives
// the digest on both issue and lookup (auth.hashSessionToken).
type WalletSession struct {
	Token     string    `json:"-" bson:"_id"`
	Address   string    `json:"address" bson:"address"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
}
