package models

import "time"

// WalletSession is an investor's short-lived self-service session
// (collection: wallet_sessions). Sessions live in the shared store rather
// than in auth.SessionManager's process-local map, so a token minted by one
// server replica validates on every other replica sharing the store, and a
// logout on ANY replica revokes it everywhere — see
// repository.WalletSessionRepository's doc comment.
//
// Token holds the SHA-256 DIGEST of the opaque bearer token,
// not the token itself — the raw token is returned to the caller once at
// issue and never persisted, so read access to this collection cannot
// reconstruct a usable credential. auth.SessionManager derives the digest on
// both issue and lookup (see auth.hashSessionToken).
type WalletSession struct {
	Token     string    `json:"-" bson:"_id"`
	Address   string    `json:"address" bson:"address"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
}
