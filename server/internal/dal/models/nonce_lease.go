package models

import "time"

// NonceLease is one distributed lease over a signer's nonce sequence
// (collection: nonce_leases). It backs multi-instance mode, where a
// distributed lock with fencing lets replicas share a hot key safely. Key
// MUST be chainId+":"+signerAddress — the lock key has to include at least
// the chainId and signer address. Token is a strictly-monotonic fencing
// token, bumped on
// every successful Acquire (fresh grant or takeover) — see
// NonceLeaseRepository.Acquire's doc comment. Deliberately never garbage
// collected by a TTL index (see the mongodb implementation's doc comment):
// an auto-deleted document would let Token silently reset, which would
// break the strict-monotonicity guarantee the whole fencing scheme depends
// on. The collection stays tiny regardless — one document per configured
// hot key, not per transaction.
type NonceLease struct {
	Key       string    `json:"key" bson:"_id"`
	HolderID  string    `json:"holderId" bson:"holderId"`
	Token     uint64    `json:"token" bson:"token"`
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}
